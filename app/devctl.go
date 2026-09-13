package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/devctl"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

// Everything in this file is remote code execution against the user's session.
// Why the socket stays off by default is on devctl.Env.

const (
	devControlDefaultTimeout = 5 * time.Second
	devControlMaxTimeout     = 60 * time.Second
)

type devControl struct {
	ln     net.Listener
	path   string
	digest [sha256.Size]byte
}

type devRequest struct {
	Token     string          `json:"token"`
	Op        string          `json:"op"`
	Args      json.RawMessage `json:"args,omitempty"`
	TimeoutMS int             `json:"timeout_ms,omitempty"`
}

type devResponse struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data,omitempty"`
	Error string          `json:"error,omitempty"`
	Code  string          `json:"code,omitempty"`
}

// Test seam: empty in production, where the path is under the real user's profile.
var devControlPathOverride string

func (a *App) startDevControl() {
	if !devctl.On(os.Getenv(devctl.Env)) {
		return
	}
	// The token says who may drive the app; the bind address keeps the question
	// off the network entirely.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		logger.Error("app: dev control listen failed", "err", err)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		_ = ln.Close()
		logger.Error("app: dev control token failed", "err", err)
		return
	}
	token := hex.EncodeToString(raw)
	dc := &devControl{ln: ln, path: a.devControlPath(), digest: sha256.Sum256([]byte(token))}
	if err := writeDevControlFile(dc.path, devctl.File{Addr: ln.Addr().String(), Token: token, PID: os.Getpid()}); err != nil {
		_ = ln.Close()
		logger.Error("app: dev control publish failed", "path", dc.path, "err", err)
		return
	}

	a.mu.Lock()
	a.devctl = dc
	a.mu.Unlock()
	// Warn, not Info: this line is what explains, in a log read later, a machine
	// that executed whatever an agent asked of it.
	logger.Warn("app: dev control socket is OPEN - an agent can run code in this app",
		"addr", ln.Addr().String(), "file", dc.path)
	go a.serveDevControl(dc)
}

func (a *App) stopDevControl() {
	a.mu.Lock()
	dc := a.devctl
	a.devctl = nil
	a.mu.Unlock()
	if dc == nil {
		return
	}
	_ = dc.ln.Close()
	// A leftover file points the next call at a dead port, which reads as a broken
	// tool rather than a stopped app.
	_ = os.Remove(dc.path)
}

func (a *App) devControlPath() string {
	if devControlPathOverride != "" {
		return devControlPathOverride
	}
	return filepath.Join(module.DataDir(a.appDataName()), devctl.FileName)
}

func writeDevControlFile(path string, f devctl.File) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 0600: the token in here is the whole authorization story.
	return os.WriteFile(path, b, 0o600)
}

func (a *App) serveDevControl(dc *devControl) {
	for {
		conn, err := dc.ln.Accept()
		if err != nil {
			return // closed by stopDevControl, or the listener is gone
		}
		go a.handleDevConn(dc, conn)
	}
}

func (a *App) handleDevConn(dc *devControl, conn net.Conn) {
	defer conn.Close()

	var req devRequest
	// One request per connection: no second frame to fall out of sync with, and a
	// peer that connects then says nothing cannot pin a goroutine.
	_ = conn.SetDeadline(time.Now().Add(devControlDefaultTimeout))
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		writeDevResponse(conn, devResponse{Error: "bad request: " + err.Error(), Code: fault.CodeInternal})
		return
	}
	if !dc.authorized(req.Token) {
		// Same answer for a wrong token and for none: the caller learns nothing
		// about which half it got wrong.
		writeDevResponse(conn, devResponse{Error: "unauthorized", Code: fault.CodeDenied})
		return
	}

	timeout := devControlDefaultTimeout
	if req.TimeoutMS > 0 {
		// Clamp before the multiply: measured, 1<<62 ms wraps to 0s, and the call
		// would then be cancelled before it ran.
		ms := min(req.TimeoutMS, int(devControlMaxTimeout/time.Millisecond))
		timeout = time.Duration(ms) * time.Millisecond
	}
	_ = conn.SetDeadline(time.Now().Add(timeout + devControlDefaultTimeout))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	data, err := a.devOp(ctx, req)
	if err != nil {
		writeDevResponse(conn, devResponse{Error: err.Error(), Code: fault.CodeOf(err)})
		return
	}
	writeDevResponse(conn, devResponse{OK: true, Data: data})
}

func (dc *devControl) authorized(candidate string) bool {
	// Fixed-size digests, so neither the token's content nor its length leaks
	// through an early return. Same reasoning as the web-mode token check.
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(got[:], dc.digest[:]) == 1
}

func writeDevResponse(conn net.Conn, res devResponse) {
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	_, _ = conn.Write(append(b, '\n'))
}

func (a *App) devOp(ctx context.Context, req devRequest) (json.RawMessage, error) {
	switch req.Op {
	case "status":
		return a.devStatus()
	case "commands":
		return json.Marshal(a.reg.Names())
	case "call":
		var p struct {
			Name    string          `json:"name"`
			Payload json.RawMessage `json:"payload"`
		}
		if len(req.Args) > 0 {
			if err := json.Unmarshal(req.Args, &p); err != nil {
				return nil, err
			}
		}
		if p.Name == "" {
			return nil, fault.New(fault.CodeNotFound, "call needs a command name")
		}
		return a.reg.Invoke(ctx, p.Name, p.Payload)
	case "eval", "dom", "input":
		client, err := a.devClient()
		if err != nil {
			return nil, err
		}
		return a.Call(ctx, client, "dev:"+req.Op, req.Args)
	case "window":
		return a.devWindow(ctx, req.Args)
	case "emit":
		return a.devEmit(req.Args)
	}
	return nil, fault.Errorf(fault.CodeNotFound, "unknown op: %q", req.Op)
}

// Every native call here, the reads included, goes through Dispatch: off the UI
// thread the failure is a native crash, not a Go error. It waits for that hop,
// or an agent that resizes and then reads the DOM measures the old layout.
func (a *App) devWindow(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Action string `json:"action"`
		W      int    `json:"w"`
		H      int    `json:"h"`
		Title  string `json:"title"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	rt, main := a.rt, a.main
	a.mu.Unlock()
	if rt == nil || main == nil {
		return nil, fault.New(fault.CodeUnavailable, "no native window: this app has none yet, or runs in web mode")
	}

	var opErr error
	var report map[string]any
	done := make(chan struct{})
	rt.Dispatch(func() {
		defer close(done)
		switch p.Action {
		case "", "info": // a bare call is a question, not a change
		case "show":
			main.Show()
		case "hide":
			main.Hide()
		case "focus":
			main.Focus()
		case "maximize":
			main.Maximize()
		case "restore":
			main.Restore()
		case "title":
			main.SetTitle(p.Title)
		case "resize":
			if p.W <= 0 || p.H <= 0 {
				opErr = fault.New(fault.CodeNotFound, "resize needs a positive w and h")
				return
			}
			main.SetSize(p.W, p.H)
		default:
			opErr = fault.Errorf(fault.CodeNotFound, "unknown window action: %q", p.Action)
			return
		}
		w, h := main.Size()
		x, y := main.Position()
		report = map[string]any{"w": w, "h": h, "x": x, "y": y, "visible": main.IsVisible(), "state": main.State()}
	})
	select {
	case <-done:
	case <-ctx.Done():
		return nil, fault.Wrap(fault.CodeCanceled, ctx.Err())
	}
	if opErr != nil {
		return nil, opErr
	}
	return json.Marshal(report)
}

// Reaches every connected client, so a frontend that only listens for events is
// still drivable.
func (a *App) devEmit(raw json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Name string          `json:"name"`
		Data json.RawMessage `json:"data"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	if p.Name == "" {
		return nil, fault.New(fault.CodeNotFound, "emit needs an event name")
	}
	a.Emit(p.Name, p.Data)
	return json.Marshal(map[string]any{"emitted": p.Name})
}

// The main window when there is one. Web mode has none, so the oldest live
// connection stands in for "the app" rather than failing outright.
func (a *App) devClient() (ClientID, error) {
	a.mu.Lock()
	main := a.main
	lowest := -1
	for id := range a.senders {
		if lowest < 0 || id < lowest {
			lowest = id
		}
	}
	a.mu.Unlock()
	if main != nil {
		return main.Client, nil
	}
	if lowest >= 0 {
		return ClientID(lowest), nil
	}
	return 0, fault.New(fault.CodeUnavailable, "no frontend is connected yet")
}

func (a *App) devStatus() (json.RawMessage, error) {
	a.mu.Lock()
	clients := len(a.senders)
	hasMain := a.main != nil
	a.mu.Unlock()
	names := a.reg.Names()
	return json.Marshal(map[string]any{
		"pid":       os.Getpid(),
		"clients":   clients,
		"has_main":  hasMain,
		"commands":  len(names),
		"dev_url":   os.Getenv("SCORIX_DEV_URL"),
		"connected": clients > 0,
	})
}

// The JS half of eval/dom/input, injected only while the socket is open so a
// shipped build cannot carry the handlers at all.
func devControlScript() string {
	if !devctl.On(os.Getenv(devctl.Env)) {
		return ""
	}
	return devCtlJS
}
