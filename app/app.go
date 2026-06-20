package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

//go:embed scorix.js
var bridgeJS string

type Options struct {
	Title      string
	Width      int
	Height     int
	Identifier string
	URL        string                // scheme selects which Serve'd FS is the root, e.g. "scorix://app/index.html"
	Security   *config.SandboxConfig // non-nil gates module commands by allowlist + applies CSP in web mode; nil disables both

	Manifest []byte // embedded scorix.yaml seeding window/app/security + modules config; explicit Options win

	// RuntimeConfigPath (or SCORIX_CONFIG env) overlays external YAML, but only onto
	// env-tagged fields; sealed fields (security, updater keys) are dropped+logged.
	// Precedence: env > runtime_file > embedded.
	RuntimeConfigPath string

	WebToken string // non-empty gates web mode behind a shared secret; empty trusts the network (loopback only). No-op in app mode.
}

type App struct {
	reg     *ipc.Registry
	opts    Options
	schemes map[string]fs.FS
	ready   []func(*App)

	cfg  *config.Config
	mods *module.Manager

	mu      sync.Mutex
	senders map[int]*senderChan // event broadcast targets (window + ws conns)
	sendSeq int
	started bool
	seq     atomic.Uint64

	rt       window.Runtime      // running native runtime (app mode); for OpenWindow/Quit
	bridges  []*ipc.NativeBridge // per-window dispatchers, drained on shutdown
	winClose sync.WaitGroup      // per-window async Close goroutines (app mode)
}

// senderChan is a per-client outbound queue drained in order by a dedicated
// goroutine, so one stalled client can't head-of-line-block the others (broadcast
// drops to a full queue instead of blocking).
type senderChan struct {
	ch   chan []byte
	done chan struct{}
}

var newDriver = defaultDriver // indirected so tests substitute the headless driver

func New(opts Options) (*App, error) {
	hasManifest := len(opts.Manifest) > 0
	cfg := &config.Config{Modules: map[string]any{}}

	if hasManifest {
		mf, err := config.FromBytes(opts.Manifest)
		if err != nil {
			return nil, fmt.Errorf("app: parse manifest: %w", err)
		}
		cfg = mf
		if cfg.Modules == nil {
			cfg.Modules = map[string]any{}
		}
	}

	// Applied even without a manifest so the no-manifest path still honors SCORIX_* env.
	raw, err := loadRuntimeOverlay(opts.RuntimeConfigPath)
	if err != nil {
		return nil, err
	}
	if err := config.ApplyOverlays(cfg, raw); err != nil {
		return nil, fmt.Errorf("app: apply runtime overlay: %w", err)
	}
	// No-manifest cfg has no CSP; "default" keeps 'unsafe-inline' for the injected bridge.
	if !hasManifest && cfg.Security.CSP == "" {
		cfg.Security.CSP = "default"
	}
	// Re-validate: the overlay can push out-of-range values past FromBytes's check.
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("app: validate config after overrides: %w", err)
	}
	var runtimeModules map[string]any
	if raw != nil {
		runtimeModules = config.AsStringMap(raw["modules"])
	}

	// Seed Options from resolved config; explicit Options win. Security seeds only
	// from a manifest — nil otherwise keeps the back-compat "no gating" default.
	if opts.Title == "" {
		opts.Title = cfg.Window.Title
	}
	if opts.Identifier == "" {
		opts.Identifier = cfg.App.Identifier
	}
	if opts.Width == 0 {
		opts.Width = cfg.Window.Width
	}
	if opts.Height == 0 {
		opts.Height = cfg.Window.Height
	}
	if hasManifest && opts.Security == nil {
		sec := cfg.Security
		opts.Security = &sec
	}

	if opts.Width == 0 {
		opts.Width = 1024
	}
	if opts.Height == 0 {
		opts.Height = 768
	}

	a := &App{
		reg:     ipc.NewRegistry(),
		opts:    opts,
		schemes: map[string]fs.FS{},
		senders: map[int]*senderChan{},
		cfg:     cfg,
	}
	if a.cfg.App.Name == "" {
		a.cfg.App.Name = opts.Identifier
	}
	if opts.Security != nil {
		a.cfg.Security = *opts.Security
	}
	a.mods = module.NewManager(a.cfg, &moduleCore{reg: a.reg, app: a}, nil)
	a.mods.SetRuntimeModules(runtimeModules)
	return a, nil
}

// loadRuntimeOverlay reads path, falling back to SCORIX_CONFIG; nil when neither is set.
func loadRuntimeOverlay(path string) (map[string]any, error) {
	if path == "" {
		path = os.Getenv("SCORIX_CONFIG")
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("app: read runtime config %q: %w", path, err)
	}
	raw, err := config.RawMap(data)
	if err != nil {
		return nil, fmt.Errorf("app: %w", err)
	}
	logger.Info("app: loaded runtime config overlay", "path", path)
	return raw, nil
}

// nil Options.Security allows every capability (back-compat).
func (a *App) allowed(capability string) bool {
	if a.opts.Security == nil {
		return true
	}
	al := a.cfg.Security.Allowlist
	switch capability {
	case "fs":
		return al.FS
	case "shell":
		return al.Shell
	case "http":
		return al.HTTP
	case "clipboard":
		return al.Clipboard
	case "notification":
		return al.Notification
	default:
		return false // fail closed: unknown capability denied. capability=="" is ungated upstream.
	}
}

// cspValue maps a symbolic security.csp to a header value; unrecognized = literal
// policy. Presets keep 'unsafe-inline' because the bridge is injected inline.
func cspValue(s string) string {
	switch s {
	case "", "none":
		return ""
	case "default":
		return "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' ws: wss:"
	case "strict":
		return "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'self'"
	default:
		return s
	}
}

// Command registers a request/reply handler callable from JS via scorix.invoke.
func (a *App) Command(name string, fn ipc.CmdFunc) { a.reg.Command(name, fn) }

// Event registers a one-way handler for scorix.emit from JS.
func (a *App) Event(name string, fn ipc.EvtFunc) { a.reg.Event(name, fn) }

func (a *App) Serve(scheme string, fsys fs.FS) {
	a.mu.Lock()
	a.schemes[scheme] = fsys
	a.mu.Unlock()
}

// OnReady runs fn once the app is up (window shown / server listening).
func (a *App) OnReady(fn func(*App)) { a.ready = append(a.ready, fn) }

func (a *App) eventEnvelope(name string, data any) ([]byte, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(webview.Message{
		ID:    "go-" + strconv.FormatUint(a.seq.Add(1), 10),
		Kind:  "event",
		Name:  name,
		State: "dispatch",
		Data:  payload,
	})
}

// Emit broadcasts a one-way event to every connected frontend (scorix.on).
func (a *App) Emit(name string, data any) {
	msg, err := a.eventEnvelope(name, data)
	if err != nil {
		logger.Error("app: emit marshal failed", "event", name, "err", err)
		return
	}
	a.broadcast(msg)
}

// EmitTo sends a one-way event to a single frontend, reporting whether it was
// still connected. Prefer over Emit for per-request follow-ups.
func (a *App) EmitTo(client ClientID, name string, data any) bool {
	msg, err := a.eventEnvelope(name, data)
	if err != nil {
		logger.Error("app: emitTo marshal failed", "event", name, "err", err)
		return false
	}
	a.mu.Lock()
	s, ok := a.senders[int(client)]
	a.mu.Unlock()
	if ok {
		s.enqueue(msg) // outside the lock: a stalled client must not block sender add/remove
	}
	return ok
}

func (a *App) broadcast(msg []byte) {
	a.mu.Lock()
	chs := make([]*senderChan, 0, len(a.senders))
	for _, s := range a.senders {
		chs = append(chs, s)
	}
	a.mu.Unlock()
	// Outside the lock: one stalled client must not freeze Emit nor block add/remove.
	for _, s := range chs {
		s.enqueue(msg)
	}
}

// enqueue offers msg without blocking; a full queue (stalled client) drops it.
func (s *senderChan) enqueue(msg []byte) {
	select {
	case s.ch <- msg:
	default:
		logger.Warn("app: dropped event — client outbound queue full")
	}
}

// addSender registers write behind a per-client queue + drainer goroutine that
// calls write in order until removeSender.
func (a *App) addSender(write func([]byte)) int {
	s := &senderChan{ch: make(chan []byte, 64), done: make(chan struct{})}
	a.mu.Lock()
	a.sendSeq++
	id := a.sendSeq
	a.senders[id] = s
	a.mu.Unlock()
	go func() {
		for {
			select {
			case b := <-s.ch:
				write(b)
			case <-s.done:
				return
			}
		}
	}()
	return id
}

func (a *App) removeSender(id int) {
	a.mu.Lock()
	s, ok := a.senders[id]
	if ok {
		delete(a.senders, id)
	}
	a.mu.Unlock()
	if ok {
		close(s.done) // map-delete guard guarantees a single close; never closes s.ch
	}
}

// Run opens the main native window and blocks on the event loop until the last
// window closes or Quit is called. Additional windows: OpenWindow.
func (a *App) Run() error {
	rt, err := newDriver().NewRuntime(window.RuntimeConfig{Identifier: a.opts.Identifier})
	if err != nil {
		return err
	}
	a.mu.Lock()
	schemes := make(map[string]fs.FS, len(a.schemes))
	for scheme, fsys := range a.schemes {
		schemes[scheme] = fsys
	}
	a.mu.Unlock()
	for scheme, fsys := range schemes {
		rt.RegisterScheme(scheme, webview.SchemeFromFS(fsys))
	}
	if err := a.startModules(); err != nil { // registers handlers into reg
		return err
	}
	defer a.stopModules()

	a.mu.Lock()
	a.rt = rt
	a.mu.Unlock()

	// SCORIX_DEV_URL (set by `scorix dev`) points at a frontend dev server for HMR.
	mainURL := a.opts.URL
	if dev := os.Getenv("SCORIX_DEV_URL"); dev != "" {
		logger.Info("app: dev mode — loading frontend from dev server", "url", dev)
		mainURL = dev
	}

	rt.On(window.RuntimeReady, func() {
		aw, err := a.attachWindow(rt, window.Options{
			Title:  a.opts.Title,
			Width:  a.opts.Width,
			Height: a.opts.Height,
			Center: true,
			URL:    mainURL,
		})
		if err != nil {
			logger.Error("app: failed to open main window — quitting", "err", err)
			rt.Quit()
			return
		}
		for _, fn := range a.ready {
			fn(a)
		}
		aw.Show()
	})
	err = rt.Run()

	// Drain in-flight handlers before the deferred stopModules tears down modules
	// (DB, etc.) underneath them; winClose covers mid-session window-close drains.
	a.mu.Lock()
	a.rt = nil
	bridges := a.bridges
	a.bridges = nil
	a.mu.Unlock()
	if len(bridges) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		for _, b := range bridges {
			_ = b.Close(ctx)
		}
		cancel()
	}
	a.winClose.Wait()
	return err
}

var upgrader = websocket.Upgrader{CheckOrigin: sameOrigin}

// sameOrigin permits the /ipc upgrade only when Origin host matches Host (or is
// absent — a non-browser client). Blocks cross-site WS hijacking.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// WebAddr is the resolved web listen address from config, defaulting to
// 127.0.0.1:8080 (loopback, secure by default).
func (a *App) WebAddr() string {
	host := a.cfg.Web.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := a.cfg.Web.Port
	if port == 0 {
		port = 8080
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// warnWebExposure warns on the two ways web mode is left open: no Security config
// (every capability allowed), and a non-loopback bind with no WebToken (all
// network clients trusted) — otherwise an operator gets no signal.
func (a *App) warnWebExposure(addr string) {
	if a.opts.Security == nil {
		logger.Warn("scorix web: no security config — all module capabilities are allowed; ship a manifest with security.allowlist to gate them")
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	if !isLoopbackHost(host) && a.opts.WebToken == "" {
		logger.Warn("scorix web: bound to a non-loopback host with no WebToken — every network client is trusted", "addr", addr)
	}
}

// isLoopbackHost reports whether host is loopback-only; empty or 0.0.0.0/:: (all
// interfaces, exposed) returns false.
func isLoopbackHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// RunWeb serves the app to browsers: assets over HTTP (bridge injected) and IPC
// over a WebSocket at /ipc. Empty addr resolves to WebAddr().
func (a *App) RunWeb(addr string) error {
	if addr == "" {
		addr = a.WebAddr()
	}
	a.warnWebExposure(addr)
	if err := a.startModules(); err != nil {
		return err
	}
	h := a.Handler()
	defer a.stopModules()
	for _, fn := range a.ready {
		fn(a)
	}
	// ReadHeaderTimeout for slowloris defense; Read/Write unset — /ipc and streaming
	// replies are long-lived.
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

const webTokenCookie = "scorix_token"

// webAuthorized reports whether a request carries the configured token (cookie,
// ?token=, or Bearer). No token configured authorizes every request.
func (a *App) webAuthorized(r *http.Request) bool {
	if a.opts.WebToken == "" {
		return true
	}
	if c, err := r.Cookie(webTokenCookie); err == nil && a.tokenEqual(c.Value) {
		return true
	}
	if q := r.URL.Query().Get("token"); q != "" && a.tokenEqual(q) {
		return true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") &&
		a.tokenEqual(strings.TrimPrefix(h, "Bearer ")) {
		return true
	}
	return false
}

// tokenEqual compares in constant time over fixed-size SHA-256 digests, so it
// leaks neither the token's content nor its length (raw ConstantTimeCompare
// returns early on a length mismatch).
func (a *App) tokenEqual(candidate string) bool {
	want := sha256.Sum256([]byte(a.opts.WebToken))
	got := sha256.Sum256([]byte(candidate))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// Handler builds the web-mode HTTP handler (assets + /ipc WebSocket). Exposed for
// mounting or httptest.
func (a *App) Handler() http.Handler {
	_ = a.startModules()
	fsys := a.rootFS()

	mux := http.NewServeMux()
	mux.HandleFunc("/ipc", func(w http.ResponseWriter, r *http.Request) {
		if !a.webAuthorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		var wmu sync.Mutex
		send := func(b []byte) error {
			wmu.Lock()
			defer wmu.Unlock()
			_ = c.SetWriteDeadline(time.Now().Add(10 * time.Second)) // bound: a stalled client must not wedge the shared sender
			return c.WriteMessage(websocket.TextMessage, b)
		}
		d := ipc.NewDispatcher(a.reg, send)
		// Broadcast (Emit) shares this write mutex but ignores the per-write error;
		// the rpc Send path propagates it.
		id := a.addSender(func(b []byte) { _ = send(b) })
		d.BindClient(ipc.ClientID(id))
		// LIFO: removeSender (stop broadcasting) runs before Close (cancel+drain handlers).
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = d.Close(ctx)
			cancel()
		}()
		defer a.removeSender(id)

		for {
			_, raw, err := c.ReadMessage()
			if err != nil {
				return
			}
			d.Handle(raw)
		}
	})
	mux.Handle("/", a.assetHandler(fsys))
	return mux
}

// rootFS picks the web-root FS by the scheme in Options.URL.
func (a *App) rootFS() fs.FS {
	a.mu.Lock()
	schemes := make(map[string]fs.FS, len(a.schemes))
	for scheme, fsys := range a.schemes {
		schemes[scheme] = fsys
	}
	a.mu.Unlock()

	if u, err := url.Parse(a.opts.URL); err == nil {
		if f, ok := schemes[u.Scheme]; ok {
			return f
		}
	}
	// Fallback only when exactly one scheme is registered.
	if len(schemes) == 1 {
		for _, f := range schemes {
			return f
		}
	}
	return nil
}

func (a *App) assetHandler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.webAuthorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// First ?token= visit → session cookie so later requests carry it.
		if a.opts.WebToken != "" && r.URL.Query().Get("token") != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     webTokenCookie,
				Value:    a.opts.WebToken,
				Path:     "/",
				HttpOnly: true,
				// Secure under TLS (direct or via a terminating proxy) so the token
				// isn't sent on a downgraded path; off for loopback dev (r.TLS nil).
				Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
				SameSite: http.SameSiteStrictMode,
			})
		}
		if fsys == nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		data, ctype, ok := readAsset(fsys, name)
		if !ok && path.Ext(name) == "" { // SPA fallback
			data, ctype, ok = readAsset(fsys, "index.html")
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(ctype, "html") {
			data = injectBridge(data)
			if csp := cspValue(a.cfg.Security.CSP); csp != "" {
				w.Header().Set("Content-Security-Policy", csp)
			}
		}
		if ctype != "" {
			w.Header().Set("Content-Type", ctype)
		}
		_, _ = w.Write(data)
	})
}

func readAsset(fsys fs.FS, name string) ([]byte, string, bool) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, "", false
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, "", false
	}
	return data, mime.TypeByExtension(path.Ext(name)), true
}

func injectBridge(html []byte) []byte {
	tag := "<script>" + bridgeJS + "</script>"
	s := string(html)
	if i := strings.Index(strings.ToLower(s), "</head>"); i >= 0 {
		return []byte(s[:i] + tag + s[i:])
	}
	return []byte(tag + s)
}
