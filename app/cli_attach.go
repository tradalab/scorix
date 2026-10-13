package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/logger"
)

const (
	cliEndpointFile = "cli-endpoint.json"
	cliRequestWait  = 5 * time.Second
	cliRequestMax   = 8 << 20
)

// The token in a 0600 file is the whole check: a program running as this user
// can read the app's data anyway. The barrier is the @cli set itself.
type cliServer struct {
	ln     net.Listener
	digest [sha256.Size]byte
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool           // set under mu, so wg is never added to while waited on
	wg     sync.WaitGroup // one per connection
}

type cliRequest struct {
	Token     string          `json:"token"`
	Command   string          `json:"command"`
	Args      json.RawMessage `json:"args"`
	Confirmed bool            `json:"confirmed"`
	TimeoutMS int64           `json:"timeout_ms,omitempty"`
}

type cliReply struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Chunk json.RawMessage `json:"chunk,omitempty"`
	Code  string          `json:"code,omitempty"`
	Error string          `json:"error,omitempty"`
}

// After the modules start: a command must not land on a closed database.
func (a *App) openCLI() {
	if !a.cfg.CLI.Enabled || len(a.cliCommands()) == 0 {
		return
	}
	if err := a.startCLI(); err != nil {
		logger.Error("app: command line socket did not open", "err", err)
		return
	}
	a.mu.Lock()
	waiting := a.cli == nil
	if waiting {
		a.cliStop = make(chan struct{})
	}
	stop := a.cliStop
	a.mu.Unlock()
	if waiting {
		go a.awaitCLI(stop, cliTakeoverEvery)
	}
}

// Without the takeover this instance runs on with no socket, and commands go
// headless beside it once the other one exits.
func (a *App) awaitCLI(stop chan struct{}, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		// A tick and a close can land together; closing wins.
		select {
		case <-stop:
			return
		default:
		}
		if err := a.startCLI(); err != nil {
			logger.Error("app: command line socket did not open", "err", err)
			return
		}
		a.mu.Lock()
		opened := a.cli != nil
		a.mu.Unlock()
		if opened {
			return
		}
	}
}

// Test seam.
var cliTakeoverEvery = 2 * time.Second

func (a *App) startCLI() error {
	dir := mcpDirOf(a.appDataName())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Overwriting a live endpoint strands that instance once this one exits.
	if cliEndpointLive(dir) {
		logger.Info("app: another instance serves the command line; not opening a second socket")
		return nil
	}
	removeDeadCLISocket(dir)
	ln, err := net.Listen("unix", socketPath(dir, "cli-"))
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		_ = ln.Close()
		return err
	}
	token := hex.EncodeToString(raw)
	if err := writeJSON0600(filepath.Join(dir, cliEndpointFile), mcpEndpoint{Addr: ln.Addr().String(), Token: token, PID: os.Getpid()}); err != nil {
		_ = ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &cliServer{ln: ln, digest: sha256.Sum256([]byte(token)), ctx: ctx, cancel: cancel}
	a.mu.Lock()
	a.cli = srv
	a.mu.Unlock()
	go a.serveCLI(srv)
	return nil
}

func cliEndpointLive(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, cliEndpointFile))
	if err != nil {
		return false
	}
	var ep mcpEndpoint
	if json.Unmarshal(b, &ep) != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", ep.Addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// One pair is left per crash. A live one is another instance's, so only a dead
// one goes.
func removeDeadCLISocket(dir string) {
	path := filepath.Join(dir, cliEndpointFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var ep mcpEndpoint
	if json.Unmarshal(b, &ep) != nil {
		return
	}
	if conn, err := net.DialTimeout("unix", ep.Addr, time.Second); err == nil {
		_ = conn.Close()
		return
	}
	if filepath.Dir(ep.Addr) == dir || filepath.Dir(ep.Addr) == os.TempDir() {
		_ = os.Remove(ep.Addr)
	}
	_ = os.Remove(path)
}

func (a *App) closeCLI() {
	a.mu.Lock()
	srv := a.cli
	a.cli = nil
	if a.cliStop != nil {
		close(a.cliStop)
		a.cliStop = nil
	}
	a.mu.Unlock()
	if srv == nil {
		return
	}
	srv.cancel()
	_ = srv.ln.Close()
	srv.mu.Lock()
	srv.closed = true
	srv.mu.Unlock()
	removeOwnEndpoint(filepath.Join(mcpDirOf(a.appDataName()), cliEndpointFile), srv.digest)
	done := make(chan struct{})
	go func() { srv.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(mcpDrainWait):
		logger.Warn("app: a command line call was still running when the app stopped waiting for it")
	}
}

func (a *App) serveCLI(srv *cliServer) {
	for {
		conn, err := srv.ln.Accept()
		if err != nil {
			return
		}
		srv.mu.Lock()
		if srv.closed {
			srv.mu.Unlock()
			_ = conn.Close()
			return
		}
		srv.wg.Add(1)
		srv.mu.Unlock()
		go func() {
			defer srv.wg.Done()
			defer conn.Close()
			a.handleCLIConn(srv, conn)
		}()
	}
}

func (a *App) handleCLIConn(srv *cliServer, conn net.Conn) {
	// A handler may send chunks from goroutines of its own.
	var wmu sync.Mutex
	write := func(r cliReply) {
		wmu.Lock()
		defer wmu.Unlock()
		b, _ := json.Marshal(r)
		_, _ = conn.Write(append(b, '\n'))
	}
	_ = conn.SetReadDeadline(time.Now().Add(cliRequestWait))
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := readCLILine(r)
	var req cliRequest
	if err != nil || json.Unmarshal(line, &req) != nil {
		write(cliReply{Code: fault.CodeDenied, Error: "unreadable request"})
		return
	}
	got := sha256.Sum256([]byte(req.Token))
	if subtle.ConstantTimeCompare(got[:], srv.digest[:]) != 1 {
		write(cliReply{Code: fault.CodeDenied, Error: "unauthorized"})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	// The socket must not reach the rest of the IPC surface.
	var cmd CLICommand
	var found bool
	for _, c := range a.cliCommands() {
		if c.Command == req.Command {
			cmd, found = c, true
		}
	}
	if !found {
		write(cliReply{Code: fault.CodeDenied, Error: req.Command + " is not offered on the command line"})
		return
	}
	// Stops a client that forgot to ask, not one that lies: the token already
	// proves a program running as this user.
	if cmd.Destructive && !req.Confirmed {
		write(cliReply{Code: fault.CodeDenied, Error: cmd.Command + " changes or deletes data and was not confirmed"})
		return
	}

	ctx, cancel := context.WithCancel(context.WithValue(srv.ctx, cliCallKey{}, true))
	defer cancel()
	timeout := cmd.Timeout
	if req.TimeoutMS > 0 {
		timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	if timeout > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, timeout)
		defer stop()
	}
	go func() {
		_, _ = r.ReadByte()
		cancel()
	}()
	res, err := a.cliCall(ctx, cmd, req.Args, func(b json.RawMessage) {
		write(cliReply{Chunk: b})
	})
	if err != nil {
		code := codeOf(ctx, err)
		// The app is closing, not the user saying no.
		if srv.ctx.Err() != nil {
			code = fault.CodeUnavailable
		}
		write(cliReply{Code: code, Error: err.Error()})
		return
	}
	write(cliReply{OK: true, Data: res})
}

func readCLILine(r *bufio.Reader) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > cliRequestMax {
			return nil, errors.New("request too large")
		}
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

func codeOf(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "timeout"
	case errors.Is(ctx.Err(), context.Canceled):
		return fault.CodeCanceled
	}
	var fe *fault.Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return fault.CodeInternal
}

// attached is false when no instance answers. Reached only after runCLI asked
// or saw --yes, hence Confirmed.
func (a *App) cliAttach(cmd CLICommand, opts cliOpts, body []byte, chunk func(json.RawMessage), errw io.Writer) (res json.RawMessage, code int, attached bool) {
	b, err := os.ReadFile(filepath.Join(mcpDirOf(a.appDataName()), cliEndpointFile))
	if err != nil {
		return nil, 0, false
	}
	var ep mcpEndpoint
	if json.Unmarshal(b, &ep) != nil {
		return nil, 0, false
	}
	conn, err := net.DialTimeout("unix", ep.Addr, time.Second)
	if err != nil {
		return nil, 0, false
	}
	defer conn.Close()
	req, _ := json.Marshal(cliRequest{Token: ep.Token, Command: cmd.Command, Args: body, Confirmed: true, TimeoutMS: opts.timeout.Milliseconds()})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, 0, false
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	type result struct {
		line []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		r := bufio.NewReader(conn)
		for {
			line, err := readCLILine(r)
			if err == nil {
				var c struct {
					Chunk json.RawMessage `json:"chunk"`
				}
				if json.Unmarshal(line, &c) == nil && c.Chunk != nil {
					chunk(c.Chunk)
					continue
				}
			}
			done <- result{line, err}
			return
		}
	}()
	var got result
	select {
	case got = <-done:
	case <-ctx.Done():
		_ = conn.Close() // the server cancels the command when we hang up
		fmt.Fprintln(errw, "error:", ctx.Err())
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ExitTimeout, true
		}
		return nil, ExitInterrupted, true
	}
	if got.err != nil {
		fmt.Fprintln(errw, "error: the running app closed the connection:", got.err)
		return nil, ExitUnavailable, true
	}
	var rep cliReply
	if err := json.Unmarshal(got.line, &rep); err != nil {
		fmt.Fprintln(errw, "error: unreadable reply from the running app")
		return nil, ExitError, true
	}
	if !rep.OK {
		fmt.Fprintln(errw, "error:", rep.Error)
		return nil, exitCodeFor(rep.Code), true
	}
	return rep.Data, ExitOK, true
}

// Test seam. Starts <App>.exe, not the .com this runs as: from the console twin
// the app would get a console window of its own.
var cliLaunch = func() error {
	exe, err := mcpSelfCommand()
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Ext(exe), ".com") {
		exe = strings.TrimSuffix(exe, filepath.Ext(exe)) + ".exe"
	}
	cmd := exec.Command(exe)
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func (a *App) cliLaunchAndWait() error {
	if err := cliLaunch(); err != nil {
		return fmt.Errorf("start %s: %w", a.cfg.App.Name, err)
	}
	path := filepath.Join(mcpDirOf(a.appDataName()), cliEndpointFile)
	deadline := time.Now().Add(mcpLaunchWait)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			var ep mcpEndpoint
			if json.Unmarshal(b, &ep) == nil {
				if conn, err := net.DialTimeout("unix", ep.Addr, time.Second); err == nil {
					_ = conn.Close()
					return nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s was started but did not answer within %s", a.cfg.App.Name, mcpLaunchWait)
}

func exitCodeFor(code string) int {
	switch code {
	case fault.CodeNotFound:
		return ExitNotFound
	case fault.CodeDenied, fault.CodeCanceled:
		return ExitDenied
	case fault.CodeUnavailable:
		return ExitUnavailable
	case fault.CodeOverloaded:
		return ExitOverloaded
	case "timeout":
		return ExitTimeout
	}
	return ExitError
}
