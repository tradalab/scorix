package app

import (
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"io"
	"io/fs"
	"mime"
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

	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/webview"
	"github.com/tradalab/scorix/window"
)

//go:embed scorix.js
var bridgeJS string

// Options configures the application window / page. It is the runtime source
// of truth; etc/app.yaml is the CLI's build manifest, not read at runtime.
type Options struct {
	Title      string
	Width      int
	Height     int
	Identifier string
	// URL is the initial page; its scheme selects which Serve'd FS is the root,
	// e.g. "scorix://app/index.html".
	URL string
	// Security, when non-nil, gates module commands by the capability allowlist
	// and applies the CSP in web mode. nil disables both.
	Security *config.SandboxConfig

	// WebToken, when set, gates web mode behind a shared secret (?token=,
	// Authorization: Bearer, or session cookie). Empty trusts the network
	// (loopback/single-user only). No effect in app mode.
	WebToken string
}

type App struct {
	reg     *ipc.Registry
	opts    Options
	schemes map[string]fs.FS
	ready   []func(*App)

	cfg  *config.Config
	mods *module.Manager

	mu      sync.Mutex
	senders map[int]func([]byte) // event broadcast targets (window + ws conns)
	sendSeq int
	started bool
	seq     atomic.Uint64

	rt      window.Runtime       // running native runtime (app mode); for OpenWindow/Quit
	bridges []*ipc.NativeBridge  // per-window dispatchers, drained on shutdown
}

// newDriver is indirected so tests can substitute the headless driver.
var newDriver = defaultDriver

// New creates an app; the backend (native window vs web server) is chosen later
// by calling Run or RunWeb.
func New(opts Options) (*App, error) {
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
		senders: map[int]func([]byte){},
		cfg:     &config.Config{Modules: map[string]any{}},
	}
	a.cfg.App.Name = opts.Identifier
	if opts.Security != nil {
		a.cfg.Security = *opts.Security
	}
	a.mods = module.NewManager(a.cfg, &moduleCore{reg: a.reg, app: a}, nil)
	return a, nil
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
		return true
	}
}

// cspValue maps the symbolic security.csp setting to a CSP header value; an
// unrecognized value is treated as a literal policy. Presets keep
// 'unsafe-inline' for scripts because the bridge is injected inline.
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

// Serve registers assets (an embed.FS) for a custom scheme / web root.
func (a *App) Serve(scheme string, fsys fs.FS) {
	a.mu.Lock()
	a.schemes[scheme] = fsys
	a.mu.Unlock()
}

// OnReady registers a callback run once the app is up (window shown / server listening).
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

// EmitTo sends a one-way event to a single frontend (identified via ClientFrom),
// reporting whether it was still connected. Prefer over Emit for per-request
// follow-ups so other clients don't receive foreign traffic.
func (a *App) EmitTo(client ClientID, name string, data any) bool {
	msg, err := a.eventEnvelope(name, data)
	if err != nil {
		logger.Error("app: emitTo marshal failed", "event", name, "err", err)
		return false
	}
	a.mu.Lock()
	fn, ok := a.senders[int(client)]
	if ok {
		fn(msg)
	}
	a.mu.Unlock()
	return ok
}

func (a *App) broadcast(msg []byte) {
	a.mu.Lock()
	for _, fn := range a.senders {
		fn(msg)
	}
	a.mu.Unlock()
}

func (a *App) addSender(fn func([]byte)) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sendSeq++
	id := a.sendSeq
	a.senders[id] = fn
	return id
}

func (a *App) removeSender(id int) {
	a.mu.Lock()
	delete(a.senders, id)
	a.mu.Unlock()
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
	if err := a.startModules(); err != nil { // OnLoad registers handlers into reg
		return err
	}
	defer a.stopModules()

	a.mu.Lock()
	a.rt = rt
	a.mu.Unlock()

	// SCORIX_DEV_URL (set by `scorix dev`) points the window at a frontend dev
	// server for HMR instead of the embedded assets; bridge still injected.
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
			rt.Quit()
			return
		}
		for _, fn := range a.ready {
			fn(a)
		}
		aw.Show()
	})
	err = rt.Run()

	// Drain in-flight handlers before module teardown so they don't race
	// modules (DB, etc.) shutting down underneath them.
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
	return err
}

var upgrader = websocket.Upgrader{CheckOrigin: sameOrigin}

// sameOrigin permits the /ipc upgrade only when Origin host matches Host (or
// Origin is absent — a non-browser client). Blocks cross-site WS hijacking.
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

// RunWeb serves the same app to browsers: assets over HTTP (with the bridge
// injected) and IPC over a WebSocket at /ipc.
func (a *App) RunWeb(addr string) error {
	if err := a.startModules(); err != nil {
		return err
	}
	h := a.Handler() // startModules is idempotent; no-op here
	defer a.stopModules()
	for _, fn := range a.ready {
		fn(a)
	}
	// Header-read timeout for slowloris defense; Read/Write left unset because
	// the /ipc WebSocket and streaming replies are long-lived.
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

const webTokenCookie = "scorix_token"

// webAuthorized reports whether a web-mode request carries the configured
// token (cookie from a prior visit, ?token= query, or Authorization: Bearer).
// With no token configured every request is authorized.
func (a *App) webAuthorized(r *http.Request) bool {
	if a.opts.WebToken == "" {
		return true
	}
	want := []byte(a.opts.WebToken)
	if c, err := r.Cookie(webTokenCookie); err == nil &&
		subtle.ConstantTimeCompare([]byte(c.Value), want) == 1 {
		return true
	}
	if q := r.URL.Query().Get("token"); q != "" &&
		subtle.ConstantTimeCompare([]byte(q), want) == 1 {
		return true
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") &&
		subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, "Bearer ")), want) == 1 {
		return true
	}
	return false
}

// Handler builds the web-mode HTTP handler (assets + /ipc WebSocket). Exposed so
// it can be mounted or tested with httptest. Module handlers are started lazily.
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
		send := func(b []byte) {
			wmu.Lock()
			_ = c.WriteMessage(websocket.TextMessage, b)
			wmu.Unlock()
		}
		d := ipc.NewDispatcher(a.reg, send)
		id := a.addSender(send)
		d.BindClient(ipc.ClientID(id))
		// On disconnect, stop broadcasting (LIFO: removeSender runs before Close)
		// then cancel+drain this connection's handlers.
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
	// Fallback only when exactly one scheme is registered; otherwise nil.
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
