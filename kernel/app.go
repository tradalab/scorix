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
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/kernel/internal/sandbox"
	"github.com/tradalab/scorix/kernel/internal/window"
	"github.com/tradalab/scorix/kernel/internal/wv"
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
	sandbox.Init(sandbox.Config{
		CSP:             cfg.Security.CSP,
		AllowRightClick: cfg.Security.AllowRightClick,
		Allowlist:       sandbox.Allowlist{
			// TODO rewrite
		},
	})

	wnd, err := wv.New(window.Config(cfg.Window))
	if err != nil {
		return nil, err
	}

	bridge := ipc.NewJSBridge(wnd)
	ipcIns := ipc.New(&bridge)
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

	a.modules = module.NewManager(cfg, a.ipc)

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

	// 1. Start embedded server with sandbox middleware
	addr, srv, err := a.startEmbeddedServer()
	if err != nil {
		return err
	}
	a.server = srv

	// 2. Load URL
	a.window.LoadURL("http://" + addr + "/")

	// 3. Run window
	a.window.Run()

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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	addr := ln.Addr().String()

	assetFs := a.cfg.AssetFs
	if a.cfg.AssetFsPath != "" {
		assetFs, err = fs.Sub(a.cfg.AssetFs, a.cfg.AssetFsPath)
		if err != nil {
			return "", nil, err
		}
	}

	handler := sandbox.SecurityMiddleware(http.FileServer(http.FS(assetFs)))

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		url := "http://" + addr
		logger.Info("embedded server running", logger.Str("url", url))
		if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatal("server failed", logger.Err(err))
		}
	}()

	return addr, server, nil
}
