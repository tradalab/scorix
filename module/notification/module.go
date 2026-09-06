package notification

import (
	"context"
	"fmt"
	"sync"

	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/module"
)

type Config struct {
	// Scheme is the app.protocols entry a click comes back on; empty resolves to
	// the first declared protocol. With none declared, Notify says so.
	Scheme string `json:"scheme"`
}

type NotificationModule struct {
	ctx    *module.Context
	cfg    Config
	scheme string
}

func New() *NotificationModule { return &NotificationModule{} }

func (m *NotificationModule) Name() string    { return "notification" }
func (m *NotificationModule) Version() string { return "1.1.0" }

func (m *NotificationModule) OnLoad(ctx *module.Context) error {
	logger.Info(fmt.Sprintf("[notification] loading (v%s)", m.Version()))
	m.ctx = ctx

	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	m.scheme = m.cfg.Scheme
	if m.scheme == "" {
		m.scheme = ctx.FirstProtocol
	}
	if m.scheme == "" {
		logger.Warn("[notification] no app.protocols scheme: notifications will show but clicks cannot come back")
	} else if ctx.App != nil {
		// Once per process: app.OnOpenURL has no unsubscribe, so a module reload
		// would leave two handlers and deliver every click twice.
		urlOnce.Do(func() {
			ctx.App.OnOpenURL(func(u string) {
				if id, action, ok := parseActivation(u); ok {
					deliver(id, action)
				}
			})
		})
	}

	subMu.Lock()
	emit = func(id, action string) {
		if err := ctx.IPC.EmitEvent(context.Background(), "activated",
			map[string]string{"id": id, "action": action}); err != nil {
			logger.Warn("[notification] cannot emit activation", "err", err)
		}
	}
	subMu.Unlock()

	module.Expose(m, "Notify", ctx.IPC)
	return nil
}

// OnStart is where a backend arranges whatever must exist before the first
// Notify. Only macOS needs it; see prepare in toast_darwin.go.
func (m *NotificationModule) OnStart() error {
	prepare()
	return nil
}

func (m *NotificationModule) OnStop() error   { return nil }
func (m *NotificationModule) OnUnload() error { return nil }

type Action struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type NotifyRequest struct {
	Title string `json:"title"`
	Text  string `json:"text"`
	Level string `json:"level,omitempty"` // info (default) | warning | error - maps to the OS icon where supported
	// ID turns the notification into something clickable: it comes back on
	// activation. Without one the notification is fire-and-forget.
	ID      string   `json:"id,omitempty"`
	Actions []Action `json:"actions,omitempty"` // buttons; a body click reports DefaultAction
}

type NotifyResponse struct {
	Clickable bool `json:"clickable"` // false when this build or config cannot deliver a click
}

// JS: scorix.invoke("mod:notification:Notify", { title: "Build done", text: "All green",
// id: "build-1", actions: [{ key: "open", label: "Open" }] })
// Clicks arrive as the event "mod:notification:activated" {id, action}.
func (m *NotificationModule) Notify(ctx context.Context, req NotifyRequest) (NotifyResponse, error) {
	// Dropping a caller's buttons quietly is the failure this module used to be.
	if len(req.Actions) > 0 && req.ID == "" {
		logger.Warn("[notification] actions dropped: they need an id to report back", "title", req.Title)
	}
	for _, a := range req.Actions {
		if a.Key == "" || a.Key == DefaultAction {
			logger.Warn("[notification] action key is empty or reserved, its click is indistinguishable from a body click",
				"key", a.Key, "label", a.Label)
		}
	}
	if err := showToast(ctx, m.appInfo(), req, m.scheme); err != nil {
		return NotifyResponse{}, err
	}
	// Whether a click can come back is the backend's fact, not this file's: macOS
	// gets the response in-process and needs no scheme at all.
	return NotifyResponse{Clickable: clickable(req, m.scheme)}, nil
}

// appInfo carries both halves because the backends need different ones: Windows
// matches the AUMID against the identifier the installer stamped on the Start
// Menu shortcut, while Linux puts the name in front of the user.
type appInfo struct{ ID, Name string }

func (a appInfo) aumid() string {
	if a.ID != "" {
		return a.ID
	}
	return a.Name
}

func (a appInfo) display() string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

func (m *NotificationModule) appInfo() appInfo {
	if m.ctx == nil {
		return appInfo{}
	}
	return appInfo{ID: m.ctx.AppIdentifier, Name: m.ctx.AppName}
}

var (
	urlOnce sync.Once
	subMu   sync.Mutex
	subs    []func(id, action string)
	// Package level because activation arrives long after Notify returned, on
	// whatever goroutine the OS handed the URL to.
	emit func(id, action string)
)

// OnActivate registers a Go callback for notification clicks. Register before
// the first Notify; fn runs off the UI thread.
func OnActivate(fn func(id, action string)) {
	subMu.Lock()
	subs = append(subs, fn)
	subMu.Unlock()
}

func deliver(id, action string) {
	subMu.Lock()
	fns := append([]func(string, string){}, subs...)
	e := emit
	subMu.Unlock()
	for _, fn := range fns {
		fn(id, action)
	}
	if e != nil {
		e(id, action)
	}
}

func (m *NotificationModule) Capability() string { return "notification" }
