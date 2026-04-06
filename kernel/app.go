package kernel

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tradalab/scorix/kernel/core/config"
	"github.com/tradalab/scorix/kernel/core/messaging/command"
	"github.com/tradalab/scorix/kernel/core/messaging/event"
	"github.com/tradalab/scorix/kernel/core/module"
	"github.com/tradalab/scorix/kernel/core/state"
	"github.com/tradalab/scorix/kernel/internal/ipc"
	"github.com/tradalab/scorix/kernel/internal/sandbox"
	"github.com/tradalab/scorix/kernel/internal/syslock"
	"github.com/tradalab/scorix/kernel/internal/window"
	"github.com/tradalab/scorix/kernel/internal/wv"
	"github.com/tradalab/scorix/logger"
)

type app struct {
	ctx     context.Context
	mu      sync.RWMutex
	closed  bool
	cfg     *config.Config
	window  window.Window
	server  *http.Server
	store   *state.Store
	ipc     *ipc.IPC
	cmd     *command.Command
	evt     *event.Event
	id      atomic.Int64
	modules *module.Manager
}

func New(initOpts []InitOption, appOpts ...AppOption) (App, error) {
	// 1. Run PreOpts
	ic := defaultInitConfig()
	for _, opt := range initOpts {
		opt(ic)
	}

	// 2. Load config
	cfg, err := loadConfig(ic)
	if err != nil {
		return nil, err
	}

	// 3. Run AppOpts
	for _, opt := range appOpts {
		opt(cfg)
	}

	// 4. init logger, sandbox, wv, setup-ipc
	logger.New(logger.Config(cfg.Logger))

	var wnd window.Window
	var bridge ipc.Bridge

	if cfg.Mode == "web" {
		bridge = ipc.NewWebBridge()
	} else {
		sandbox.Init(sandbox.Config{
			CSP:             cfg.Security.CSP,
			AllowRightClick: cfg.Security.AllowRightClick,
			Allowlist:       sandbox.Allowlist{
				FS:           cfg.Security.Allowlist.FS,
				Shell:        cfg.Security.Allowlist.Shell,
				HTTP:         cfg.Security.Allowlist.HTTP,
				Clipboard:    cfg.Security.Allowlist.Clipboard,
				Notification: cfg.Security.Allowlist.Notification,
			},
		})

		wnd, err = wv.New(window.Config(cfg.Window))
		if err != nil {
			return nil, err
		}
		bridge = ipc.NewAppBridge(wnd)
	}

	ipcIns := ipc.New(bridge)
	ipcIns.Start()

	// 5. Init app (with module manager)
	a := &app{
		ctx:    context.Background(),
		cfg:    cfg,
		window: wnd,
		store:  state.New(),
		ipc:    ipcIns,
		cmd:    command.New(ipcIns),
		evt:    event.New(ipcIns),
	}

	// 6. in web mode, window is nil so app-level Show/Close are not available
	var appCtrl module.AppController
	if cfg.Mode != "web" {
		appCtrl = a
	}

	a.modules = module.NewManager(cfg, a.ipc, appCtrl)

	return a, nil
}

func MustNew(initOpts []InitOption, appOpts ...AppOption) App {
	a, err := New(initOpts, appOpts...)
	if err != nil {
		panic(err)
	}
	return a
}

func loadConfig(ic *InitConfig) (*config.Config, error) {
	if len(ic.Data) > 0 {
		return config.FromBytes(ic.Data)
	}
	return config.Load(ic.Path)
}

func (a *app) Cfg() *config.Config { return a.cfg }

func (a *app) Store() *state.Store { return a.store }

func (a *app) Modules() *module.Manager { return a.modules }

func (a *app) Run() error {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("panic: %v", r))
		}
	}()

	// 0. Load & start all enabled modules
	if err := a.modules.LoadAll(); err != nil {
		return err
	}
	if err := a.modules.StartAll(); err != nil {
		return err
	}

	// 1. Check Single Instance Lock
	if a.cfg.App.SingleInstance {
		isPrimary := syslock.Acquire(a.cfg.App.Identifier, func() {
			if a.window != nil {
				a.window.Show()
			}
		})
		if !isPrimary {
			// A secondary instance just forwarded FOCUS to primary. Exit cleanly.
			os.Exit(0)
		}
	}

	// 2. Start embedded server with sandbox middleware
	addr, srv, err := a.startEmbeddedServer()
	if err != nil {
		return err
	}
	a.server = srv

	if a.cfg.Mode == "web" {
		// wait
		logger.Info("app running in web mode", logger.Str("addr", addr))
		select {}
	} else {
		// 2. Load URL
		a.window.LoadURL("http://" + addr + "/")

		// 3. Set Hide on close
		if a.cfg.Window.HideOnClose {
			a.window.SetHideOnClose(true)
		}

		// 4. Run window
		a.window.Run()
	}

	return nil
}

func (a *app) Cmd() *command.Command {
	return a.cmd
}

func (a *app) Evt() *event.Event {
	return a.evt
}

func (a *app) Close() {
	logger.Info("app closing")

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true

	if a.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		a.server.Shutdown(ctx)
	}

	if a.window != nil {
		a.window.SetHideOnClose(false)
		a.window.Close()
	}

	// Stop & unload all modules (reverse order)
	a.modules.StopAll()
	a.modules.UnloadAll()

	logger.Info("app closed")
	os.Exit(0)
}

func (a *app) Show() {
	a.window.Show()
}

func (a *app) startEmbeddedServer() (string, *http.Server, error) {
	host := a.cfg.Web.Host
	if host == "" {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, a.cfg.Web.Port))
	if err != nil {
		logger.Warn("configured port bind failed, falling back to random port", logger.Err(err))
		ln, err = net.Listen("tcp", fmt.Sprintf("%s:0", host))
		if err != nil {
			return "", nil, err
		}
	}
	addr := ln.Addr().String()

	assetFs := a.cfg.AssetFs
	if a.cfg.AssetFsPath != "" {
		assetFs, err = fs.Sub(a.cfg.AssetFs, a.cfg.AssetFsPath)
		if err != nil {
			return "", nil, err
		}
	}

	mux := http.NewServeMux()
	mux.Handle("/", sandbox.SecurityMiddleware(http.FileServer(http.FS(assetFs))))

	// route /ipc to WebBridge
	if webBridge, ok := a.ipc.Bridge().(*ipc.WebBridge); ok {
		mux.Handle("/ipc", webBridge)
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		url := "http://" + addr
		logger.Info("server running", logger.Str("url", url), logger.Str("mode", a.cfg.Mode))
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed asynchronously", logger.Err(err))
			a.Close() // Gracefully shutdown the app if the embedded server dies
		}
	}()

	return addr, server, nil
}
