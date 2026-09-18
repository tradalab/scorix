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
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ncruces/zenity"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/internal/mcpcore"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

// MCPTool is registered by the code `generate proto` emits for an @mcp rpc.
type MCPTool struct {
	Name        string
	Command     string
	Description string
	InputSchema json.RawMessage
	// Destructive asks the user in a native dialog before every call.
	Destructive  bool
	OutputSchema json.RawMessage
	Timeout      time.Duration
}

// MCPCall has no frontend behind it: ClientFrom reports none, and a generated
// EmitXCtx broadcasts to every window instead.
type MCPCall struct {
	Tool   string
	Client string // what the MCP client called itself: self-reported, so it names and never authorizes
	// ClientPath is the executable the OS reports, and what the user approved.
	ClientPath string
}

type mcpCallKey struct{}

func MCPCallFrom(ctx context.Context) (MCPCall, bool) {
	c, ok := ctx.Value(mcpCallKey{}).(MCPCall)
	return c, ok
}

// MCPProgress is false outside an MCP call, or when the client asked for none.
func MCPProgress(ctx context.Context, message string) bool { return mcpcore.Progress(ctx, message) }

const (
	mcpSettingsFile = "mcp.json"
	mcpEndpointFile = "mcp-endpoint.json"
	mcpAuditFile    = "mcp-audit.log"
	mcpAuditMax     = 1 << 20 // rotated once, so the log never outgrows two files
	mcpHelloWait    = 5 * time.Second
	mcpArgsShown    = 512
	// Past it, a command that ignores its context is left to finish on its own.
	mcpDrainWait = 5 * time.Second
)

// Test seam.
var mcpDirOverride string

func mcpDirOf(appName string) string {
	if mcpDirOverride != "" {
		return mcpDirOverride
	}
	return module.DataDir(appName)
}

func (a *App) MCPTool(t MCPTool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mcpTools = append(a.mcpTools, t)
}

type mcpServer struct {
	ln     net.Listener
	digest [sha256.Size]byte
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	conns  map[net.Conn]string // to the client's path, once known
	closed bool                // set under mu with the last Add, so wg is never added to while waited on
	wg     sync.WaitGroup      // one per connection, which Serve holds until its calls return
	// One question per program at a time, however many connections it opens.
	asking map[string]chan struct{}
	// Answered Deny: not asked again until MCP is switched off and on.
	refused map[string]bool
}

type mcpSession struct {
	srv  *mcpServer
	peer mcpPeer
	gone context.Context
}

type mcpEndpoint struct {
	Addr  string `json:"addr"`
	Token string `json:"token"`
	PID   int    `json:"pid"`
}

type mcpSettings struct {
	Enabled         bool                `json:"enabled"`
	DisabledTools   []string            `json:"disabled_tools,omitempty"`
	ApprovedClients []mcpApprovedClient `json:"approved_clients,omitempty"`
}

type mcpApprovedClient struct {
	Path       string    `json:"path"`
	Name       string    `json:"name,omitempty"` // what it called itself when the user said yes
	ApprovedAt time.Time `json:"approved_at"`
}

// Only app mode: web mode has no user at a screen to confirm a destructive call.
func (a *App) openMCP() {
	a.mu.Lock()
	a.mcpHost = true
	idle := a.cfg.MCP.Enabled && len(a.mcpTools) == 0
	a.mu.Unlock()
	if idle {
		logger.Warn("app: mcp.enabled is on but no command is @mcp, so there is nothing to serve")
	}
	if err := a.startMCP(); err != nil {
		logger.Error("app: MCP socket did not open", "err", err)
	}
}

func (a *App) closeMCP() {
	a.mu.Lock()
	a.mcpHost = false
	a.mu.Unlock()
	a.stopMCP(true)
}

func (a *App) mcpOffered() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg.MCP.Enabled && len(a.mcpTools) > 0
}

func (a *App) startMCP() error {
	a.mcpMu.Lock()
	defer a.mcpMu.Unlock()
	if !a.mcpOffered() || !mcpUserEnabled(a.appDataName()) {
		return nil
	}
	a.mu.Lock()
	if !a.mcpHost || a.mcp != nil {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()

	// A unix socket, not loopback TCP: the OS names the process at the other end.
	dir := mcpDirOf(a.appDataName())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ln, err := net.Listen("unix", mcpSocketPath(dir))
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		_ = ln.Close()
		return err
	}
	token := hex.EncodeToString(raw)
	ep := mcpEndpoint{Addr: ln.Addr().String(), Token: token, PID: os.Getpid()}
	if err := writeJSON0600(filepath.Join(dir, mcpEndpointFile), ep); err != nil {
		_ = ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := &mcpServer{ln: ln, digest: sha256.Sum256([]byte(token)), ctx: ctx, cancel: cancel,
		conns: map[net.Conn]string{}, asking: map[string]chan struct{}{}, refused: map[string]bool{}}

	a.mu.Lock()
	a.mcp = srv
	tools := len(a.mcpTools)
	a.mu.Unlock()
	logger.Info("app: MCP socket open", "addr", ep.Addr, "tools", tools)
	go a.serveMCP(srv)
	return nil
}

// Drops open connections too: off means the agent stops now, not at its next
// reconnect. drain waits for the calls they were running, which only quitting needs.
func (a *App) stopMCP(drain bool) {
	a.mcpMu.Lock()
	a.mu.Lock()
	srv := a.mcp
	a.mcp = nil
	a.mu.Unlock()
	if srv == nil {
		a.mcpMu.Unlock()
		return
	}
	srv.cancel()
	_ = srv.ln.Close()
	srv.mu.Lock()
	srv.closed = true
	for c := range srv.conns {
		_ = c.Close()
	}
	srv.mu.Unlock()
	// A leftover endpoint sends the relay to a dead port instead of starting the app.
	removeOwnEndpoint(filepath.Join(mcpDirOf(a.appDataName()), mcpEndpointFile), srv.digest)
	a.mcpMu.Unlock()

	if !drain {
		return
	}
	done := make(chan struct{})
	go func() { srv.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(mcpDrainWait):
		logger.Warn("app: an MCP call was still running when the app stopped waiting for it")
	}
}

// Only our own: with single_instance off, another instance may be serving now.
func removeOwnEndpoint(path string, digest [sha256.Size]byte) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var ep mcpEndpoint
	if json.Unmarshal(b, &ep) != nil || sha256.Sum256([]byte(ep.Token)) != digest {
		return
	}
	_ = os.Remove(path)
}

func (a *App) serveMCP(srv *mcpServer) {
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
		srv.conns[conn] = ""
		srv.wg.Add(1)
		srv.mu.Unlock()
		go func() {
			defer srv.wg.Done()
			defer func() {
				srv.mu.Lock()
				delete(srv.conns, conn)
				srv.mu.Unlock()
				_ = conn.Close()
			}()
			a.handleMCPConn(srv, conn)
		}()
	}
}

func (a *App) handleMCPConn(srv *mcpServer, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(mcpHelloWait))
	r := bufio.NewReader(conn)
	// ReadSlice, not ReadBytes: an endless line fails at once, not at the deadline.
	line, err := r.ReadSlice('\n')
	var hello struct {
		Token string `json:"token"`
	}
	if err != nil || json.Unmarshal(line, &hello) != nil || !srv.authorized(hello.Token) {
		_, _ = conn.Write([]byte(`{"ok":false,"error":"unauthorized"}` + "\n"))
		return
	}
	peer, err := mcpIdentify(conn)
	if err != nil {
		logger.Warn("app: MCP connection refused, its program could not be identified", "err", err)
		refusal, _ := json.Marshal(map[string]any{"ok": false, "error": "could not tell which program is connecting: " + err.Error()})
		_, _ = conn.Write(append(refusal, '\n'))
		return
	}
	srv.mu.Lock()
	srv.conns[conn] = peer.Path
	srv.mu.Unlock()
	if _, err := conn.Write([]byte(`{"ok":true}` + "\n")); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	a.mu.Lock()
	name, version := a.cfg.App.Name, a.cfg.App.Version
	a.mu.Unlock()
	gone, clientGone := context.WithCancel(srv.ctx)
	defer clientGone()
	s := &mcpSession{srv: srv, peer: peer, gone: gone}
	server := mcpcore.New(mcpcore.Options{Name: name, Version: version, Tools: a.mcpcoreTools(s)})
	_ = server.Serve(srv.ctx, readUntilGone{r, clientGone}, conn)
}

func (srv *mcpServer) dropClient(path string) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for c, p := range srv.conns {
		if p != "" && samePath(p, path) {
			_ = c.Close()
		}
	}
}

// mcpcore lets running calls finish for up to a minute, which is right for work
// and wrong for a question put to the user on the agent's behalf.
type readUntilGone struct {
	r    io.Reader
	gone context.CancelFunc
}

func (g readUntilGone) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if err != nil {
		g.gone()
	}
	return n, err
}

func (srv *mcpServer) authorized(candidate string) bool {
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(got[:], srv.digest[:]) == 1
}

// Listed once per connection, which is why mcpRun asks the per-tool switch again.
func (a *App) mcpcoreTools(s *mcpSession) []mcpcore.Tool {
	a.mu.Lock()
	tools := append([]MCPTool(nil), a.mcpTools...)
	a.mu.Unlock()
	off := loadMCPSettings(a.appDataName()).DisabledTools
	out := make([]mcpcore.Tool, 0, len(tools))
	for _, t := range tools {
		if slices.Contains(off, t.Name) {
			continue
		}
		schema := schemaMap(t.InputSchema)
		if schema == nil {
			schema = map[string]any{"type": "object"}
		}
		out = append(out, mcpcore.Tool{
			Name:         t.Name,
			Description:  t.Description,
			Schema:       schema,
			OutputSchema: schemaMap(t.OutputSchema),
			Timeout:      t.Timeout,
			Run:          a.mcpRun(t, s),
			// Always sent: the spec defaults destructiveHint to true.
			Annotations: map[string]any{"destructiveHint": t.Destructive},
		})
	}
	return out
}

func schemaMap(raw json.RawMessage) map[string]any {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func (a *App) mcpRun(t MCPTool, s *mcpSession) func(context.Context, mcpcore.Args, io.Writer) error {
	return func(ctx context.Context, args mcpcore.Args, out io.Writer) error {
		call := MCPCall{Tool: t.Name, Client: mcpcore.ClientName(ctx), ClientPath: s.peer.Path}
		ctx = context.WithValue(ctx, mcpCallKey{}, call)
		payload := []byte("{}")
		if args != nil {
			b, err := json.Marshal(map[string]any(args))
			if err != nil {
				return writeMCPError(out, err)
			}
			payload = b
		}
		shown := redactArgs(payload)
		start := time.Now()
		if slices.Contains(loadMCPSettings(a.appDataName()).DisabledTools, t.Name) {
			err := fault.Errorf(fault.CodeDenied, "the user turned %s off in the app", t.Name)
			a.mcpRecord(call, shown, start, "denied", err)
			return writeMCPError(out, err)
		}
		if !a.mcpClientAllowed(ctx, s, call) {
			err := fault.Errorf(fault.CodeDenied, "the user has not allowed %s to use this app", s.peer.Path)
			a.mcpRecord(call, shown, start, "denied", err)
			return writeMCPError(out, err)
		}
		if t.Destructive && !a.mcpAllowed(ctx, s.gone, call, t, shown) {
			err := fault.Errorf(fault.CodeDenied, "the user did not allow %s", t.Name)
			a.mcpRecord(call, shown, start, "denied", err)
			return writeMCPError(out, err)
		}
		res, err := a.reg.Invoke(ctx, t.Command, payload)
		if err != nil {
			a.mcpRecord(call, shown, start, "error", err)
			return writeMCPError(out, err)
		}
		a.mcpRecord(call, shown, start, "ok", nil)
		if len(res) == 0 || string(res) == "null" {
			res = json.RawMessage("null")
			if len(t.OutputSchema) > 0 {
				// An outputSchema promises an object, and the generated ones require no field.
				res = json.RawMessage("{}")
			}
		}
		_, err = out.Write(res)
		return err
	}
}

// Asked at the first call, not at connect: a client starts its servers at launch,
// and a dialog nobody caused then is answered without reading.
func (a *App) mcpClientAllowed(ctx context.Context, s *mcpSession, call MCPCall) bool {
	key := mcpPathKey(s.peer.Path)
	for {
		if a.mcpApproved(s.peer.Path) {
			return true
		}
		s.srv.mu.Lock()
		if s.srv.refused[key] {
			s.srv.mu.Unlock()
			return false
		}
		wait, busy := s.srv.asking[key]
		if !busy {
			wait = make(chan struct{})
			s.srv.asking[key] = wait
		}
		s.srv.mu.Unlock()
		if !busy {
			return a.mcpAskAboutClient(ctx, s, call, key, wait)
		}
		select {
		case <-wait:
			// Answered, or dropped because the connection that asked went away; look again.
		case <-ctx.Done():
			return false
		case <-s.gone.Done():
			return false
		}
	}
}

func (a *App) mcpAskAboutClient(ctx context.Context, s *mcpSession, call MCPCall, key string, wait chan struct{}) bool {
	defer func() {
		s.srv.mu.Lock()
		delete(s.srv.asking, key)
		s.srv.mu.Unlock()
		close(wait)
	}()
	ask, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(s.gone, stop)()
	ok := mcpConfirm(ask, a.appDataName(), mcpConsentText(a.appDataName(), s.peer, call))
	if ask.Err() != nil {
		return false // nobody answered, so nothing is remembered
	}
	if !ok {
		s.srv.mu.Lock()
		s.srv.refused[key] = true
		s.srv.mu.Unlock()
		return false
	}
	err := a.updateMCPSettings(func(st *mcpSettings) {
		st.ApprovedClients = append(st.ApprovedClients, mcpApprovedClient{Path: s.peer.Path, Name: call.Client, ApprovedAt: time.Now().UTC()})
	})
	if err != nil {
		logger.Warn("app: the MCP client was allowed but not remembered, so it will be asked about again", "path", s.peer.Path, "err", err)
	}
	return true
}

func (a *App) mcpApproved(path string) bool {
	return slices.ContainsFunc(loadMCPSettings(a.appDataName()).ApprovedClients, func(c mcpApprovedClient) bool {
		return samePath(c.Path, path)
	})
}

func mcpConsentText(appName string, peer mcpPeer, call MCPCall) string {
	text := fmt.Sprintf("A program wants to use the tools %s offers to AI agents.\n\nProgram: %s", appName, peer.Path)
	if call.Client != "" {
		text += fmt.Sprintf("\nIt calls itself: %q", call.Client)
	}
	return text + "\n\nAllow is remembered for this program, and covers anything it runs, until you remove it in the app's settings."
}

// An Allow clicked after the agent left would delete for no one.
func (a *App) mcpAllowed(ctx, gone context.Context, call MCPCall, t MCPTool, shown []byte) bool {
	ask, stop := context.WithCancel(ctx)
	defer stop()
	defer context.AfterFunc(gone, stop)()
	return mcpConfirm(ask, a.appDataName(), mcpConfirmText(call, t, shown)) && gone.Err() == nil
}

func writeMCPError(out io.Writer, err error) error {
	b, _ := json.Marshal(map[string]string{"error": err.Error(), "code": fault.CodeOf(err)})
	_, _ = out.Write(b)
	return err
}

func mcpConfirmText(call MCPCall, t MCPTool, shown []byte) string {
	who := "An AI agent"
	if call.Client != "" {
		who = fmt.Sprintf("The MCP client %q", call.Client)
	}
	return fmt.Sprintf("%s wants to run %q.\n\n%s\n\nArguments: %s\n\nProgram: %s", who, t.Name, t.Description, clip(shown, mcpArgsShown), call.ClientPath)
}

// Matched inside a key, case-insensitively, so api_token and dbPassword are caught.
// A bare "key" is not: in a Redis or S3 tool that is the data, not a credential.
var mcpSecretNames = []string{"password", "passwd", "secret", "token", "credential", "apikey", "api_key", "private", "authorization"}

// Hidden from the dialog and the audit log; the command still gets them as sent.
func redactArgs(payload []byte) []byte {
	var v any
	if json.Unmarshal(payload, &v) != nil {
		return payload
	}
	b, err := json.Marshal(redactValue(v))
	if err != nil {
		return payload
	}
	return b
}

func redactValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if isSecretName(k) {
				x[k] = "[redacted]"
				continue
			}
			x[k] = redactValue(val)
		}
	case []any:
		for i := range x {
			x[i] = redactValue(x[i])
		}
	}
	return v
}

func isSecretName(key string) bool {
	key = strings.ToLower(key)
	for _, s := range mcpSecretNames {
		if strings.Contains(key, s) {
			return true
		}
	}
	return false
}

// Fails closed: a missing zenity or kdialog is an error, not consent.
var mcpConfirm = func(ctx context.Context, title, text string) bool {
	err := zenity.Question(text, zenity.Title(title), zenity.OKLabel("Allow"), zenity.CancelLabel("Deny"),
		zenity.WarningIcon, zenity.Context(ctx))
	if err != nil && !errors.Is(err, zenity.ErrCanceled) {
		logger.Warn("app: MCP confirmation could not be shown, the call is refused", "err", err)
	}
	return err == nil
}

type mcpCallEvent struct {
	Tool       string `json:"tool"`
	Client     string `json:"client"`
	ClientPath string `json:"client_path"`
	Outcome    string `json:"outcome"` // ok | error | denied
	Error      string `json:"error,omitempty"`
	MS         int64  `json:"ms"`
}

// The event is for app screens that may show data the agent just changed.
func (a *App) mcpRecord(call MCPCall, shown []byte, start time.Time, outcome string, err error) {
	ev := mcpCallEvent{Tool: call.Tool, Client: call.Client, ClientPath: call.ClientPath, Outcome: outcome, MS: time.Since(start).Milliseconds()}
	if err != nil {
		ev.Error = err.Error()
	}
	a.Emit("sys:mcp:call", ev)
	a.mcpAudit(start, ev, shown)
}

var mcpAuditMu sync.Mutex

// A line that cannot be written is logged, and the call still returns.
func (a *App) mcpAudit(start time.Time, ev mcpCallEvent, shown []byte) {
	entry := map[string]any{
		"time":        start.UTC().Format(time.RFC3339),
		"tool":        ev.Tool,
		"client":      ev.Client,
		"client_path": ev.ClientPath,
		"outcome":     ev.Outcome,
		"ms":          ev.MS,
		"args":        clip(shown, mcpArgsShown),
	}
	if ev.Error != "" {
		entry["error"] = ev.Error
	}
	line, _ := json.Marshal(entry)
	path := filepath.Join(mcpDirOf(a.appDataName()), mcpAuditFile)

	mcpAuditMu.Lock()
	defer mcpAuditMu.Unlock()
	if fi, statErr := os.Stat(path); statErr == nil && fi.Size() > mcpAuditMax {
		_ = os.Rename(path, path+".1")
	}
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if openErr == nil {
		_, openErr = f.Write(append(line, '\n'))
		_ = f.Close()
	}
	if openErr != nil {
		logger.Warn("app: MCP audit line not written", "path", path, "err", openErr)
	}
}

func clip(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	// On a character boundary: half a character shows up in the dialog as garbage.
	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}
	return string(b[:n]) + "..."
}

// A missing or unreadable file reads as everything off.
func loadMCPSettings(appName string) mcpSettings {
	var s mcpSettings
	if b, err := os.ReadFile(filepath.Join(mcpDirOf(appName), mcpSettingsFile)); err == nil {
		if json.Unmarshal(b, &s) != nil {
			return mcpSettings{}
		}
	}
	return s
}

func mcpUserEnabled(appName string) bool { return loadMCPSettings(appName).Enabled }

// Held across read, change and write: two switches can arrive together.
var mcpSettingsMu sync.Mutex

func (a *App) updateMCPSettings(change func(*mcpSettings)) error {
	mcpSettingsMu.Lock()
	defer mcpSettingsMu.Unlock()
	s := loadMCPSettings(a.appDataName())
	change(&s)
	return writeJSON0600(filepath.Join(mcpDirOf(a.appDataName()), mcpSettingsFile), s)
}

func writeJSON0600(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// 0600: the endpoint holds the token, and mcp.json says which programs are allowed.
	return os.WriteFile(path, b, 0o600)
}

type mcpToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Destructive bool   `json:"destructive"`
	Enabled     bool   `json:"enabled"`
}

type mcpStatus struct {
	// Offered: mcp.enabled in the manifest and at least one @mcp command.
	Offered bool                `json:"offered"`
	Enabled bool                `json:"enabled"`
	Running bool                `json:"running"`
	Tools   []mcpToolInfo       `json:"tools"`
	Clients []mcpApprovedClient `json:"clients"`
	// Client is what to put in an MCP client's config to reach this app.
	Client struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	} `json:"client"`
	AuditLog string `json:"audit_log"`
}

func (a *App) mcpStatus() mcpStatus {
	var s mcpStatus
	s.Offered = a.mcpOffered()
	settings := loadMCPSettings(a.appDataName())
	s.Enabled = settings.Enabled
	a.mu.Lock()
	s.Running = a.mcp != nil
	s.Tools = make([]mcpToolInfo, 0, len(a.mcpTools))
	for _, t := range a.mcpTools {
		s.Tools = append(s.Tools, mcpToolInfo{Name: t.Name, Description: t.Description, Destructive: t.Destructive,
			Enabled: !slices.Contains(settings.DisabledTools, t.Name)})
	}
	a.mu.Unlock()
	s.Clients = append([]mcpApprovedClient{}, settings.ApprovedClients...)
	s.Client.Command, _ = mcpSelfCommand()
	s.Client.Args = []string{"--mcp"}
	s.AuditLog = filepath.Join(mcpDirOf(a.appDataName()), mcpAuditFile)
	return s
}

// An AppImage's mount is gone once the process exits; the image itself lasts.
func mcpSelfCommand() (string, error) {
	if p := os.Getenv("APPIMAGE"); p != "" {
		return p, nil
	}
	return os.Executable()
}

func (a *App) registerMCPCommands() {
	a.reg.Command("sys:mcp:status", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		return a.mcpStatus(), nil
	})
	a.reg.Command("sys:mcp:enable", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		var req struct {
			Enabled bool `json:"enabled"`
		}
		if len(data) > 0 {
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
		}
		if req.Enabled && !a.mcpOffered() {
			return nil, fault.New(fault.CodeDenied, "this app offers no MCP tools: mcp.enabled is off in scorix.yaml, or no command is @mcp")
		}
		if err := a.updateMCPSettings(func(s *mcpSettings) { s.Enabled = req.Enabled }); err != nil {
			return nil, err
		}
		if req.Enabled {
			if err := a.startMCP(); err != nil {
				return nil, err
			}
		} else {
			a.stopMCP(false)
		}
		return a.mcpStatus(), nil
	})
	a.reg.Command("sys:mcp:tool", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		var req struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, err
		}
		a.mu.Lock()
		known := slices.ContainsFunc(a.mcpTools, func(t MCPTool) bool { return t.Name == req.Name })
		a.mu.Unlock()
		if !known {
			return nil, fault.Errorf(fault.CodeNotFound, "no MCP tool is named %q", req.Name)
		}
		err := a.updateMCPSettings(func(s *mcpSettings) {
			s.DisabledTools = slices.DeleteFunc(s.DisabledTools, func(n string) bool { return n == req.Name })
			if !req.Enabled {
				s.DisabledTools = append(s.DisabledTools, req.Name)
			}
		})
		if err != nil {
			return nil, err
		}
		return a.mcpStatus(), nil
	})
	// Only a revoke: a program is allowed from the dialog its first call raises.
	a.reg.Command("sys:mcp:revoke", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		var req struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(data, &req); err != nil {
			return nil, err
		}
		found := false
		err := a.updateMCPSettings(func(s *mcpSettings) {
			n := len(s.ApprovedClients)
			s.ApprovedClients = slices.DeleteFunc(s.ApprovedClients, func(c mcpApprovedClient) bool { return samePath(c.Path, req.Path) })
			found = len(s.ApprovedClients) < n
		})
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fault.Errorf(fault.CodeNotFound, "no approved MCP client is at %q", req.Path)
		}
		a.mu.Lock()
		srv := a.mcp
		a.mu.Unlock()
		if srv != nil {
			srv.dropClient(req.Path)
		}
		return a.mcpStatus(), nil
	})
}
