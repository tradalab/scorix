package kernel

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/tradalab/scorix/kernel/core/config"
	"github.com/tradalab/scorix/kernel/core/extension"
	_ "github.com/tradalab/scorix/kernel/core/extensions"
	"github.com/tradalab/scorix/kernel/core/ipc"
	"github.com/tradalab/scorix/kernel/core/ipc/event"
	"github.com/tradalab/scorix/kernel/core/ipc/invoke"
	"github.com/tradalab/scorix/kernel/core/ipc/resolve"
	"github.com/tradalab/scorix/kernel/core/plugin"
	"github.com/tradalab/scorix/kernel/core/state"
	"github.com/tradalab/scorix/kernel/internal/logger"
	"github.com/tradalab/scorix/kernel/internal/sandbox"
	"github.com/tradalab/scorix/kernel/internal/window"
	"github.com/tradalab/scorix/kernel/internal/wv"
	"github.com/tradalab/scorix/kernel/internal/ze"
)

type app struct {
	ctx     context.Context
	mu      sync.RWMutex
	closed  bool
	cfg     *config.Config
	window  window.Window
	server  *http.Server
	store   *state.Store
	plugins []plugin.Plugin
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

	a := &app{
		ctx:    context.Background(),
		cfg:    cfg,
		window: wnd,
		store:  state.New(),
	}

	if err := a.setupIPC(); err != nil {
		return nil, err
	}

	// 5. Load extensions
	a.ctx = context.WithValue(a.ctx, extension.KeyConfig, a.cfg.Raw)
	a.ctx = context.WithValue(a.ctx, extension.KeyApp, a)

	if err := extension.LoadExtensions(a.ctx); err != nil {
		return nil, err
	}

	//// 6. Load plugins // todo rewrite
	//plugin.GlobalRegistry.App = a
	//if err := plugin.GlobalRegistry.StartAll(); err != nil {
	//	return nil, err
	//}

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

func (a *app) Run() error {
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

func (a *app) On(topic string, handler func(data any)) func() {
	return event.Subscribe(topic, handler)
}

func (a *app) Expose(name string, handler any) {
	hv := reflect.ValueOf(handler)
	ht := hv.Type()

	// 1. Validate function signature
	if ht.Kind() != reflect.Func {
		panic("app expose: handler must be a function")
	}
	if ht.NumIn() != 2 {
		panic("app expose: handler must have 2 arguments: (context.Context, args)")
	}
	if ht.NumOut() != 2 {
		panic("app expose: handler must return (any, error)")
	}

	// in0 must be context.Context
	if ht.In(0) != reflect.TypeOf((*context.Context)(nil)).Elem() {
		panic("app expose: first argument must be context.Context")
	}

	// out1 must be error
	if ht.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		panic("app expose: second return value must be error")
	}

	argType := ht.In(1)

	// 2. Wrapper for invoke system
	wrapped := func(ctx context.Context, raw json.RawMessage) (any, error) {
		// decode args to real type
		argVal, err := ze.DecodeArg(raw, argType)
		if err != nil {
			return nil, err
		}

		// call handler(ctx, args)
		res := hv.Call([]reflect.Value{
			reflect.ValueOf(ctx),
			argVal,
		})

		// return (value, error)
		if !res[1].IsNil() {
			return nil, res[1].Interface().(error)
		}
		return res[0].Interface(), nil
	}

	// 3. Register invoke
	invoke.Register(name, wrapped)
}

func (a *app) Resolve(name string, params any) {
	resolve.CallJS(a.window, name, params)
}

func (a *app) Emit(topic string, data any) {
	event.PublishJS(a.window, topic, data)
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	extension.StopExtensions(ctx)

	logger.Info("app closed")
	os.Exit(0)
}

func (a *app) Show() {
	a.window.Show()
}

func (a *app) setupIPC() error {
	//bridgeJS, err := fs.ReadFile(a.assets, "core/ipc/bridge.js")
	//if err != nil {
	//	return err
	//}
	//
	//a.window.OnNavigated(func() {
	//	a.window.Eval(string(bridgeJS))
	//})

	a.window.Bind("__scorix_bind_invoke", func(payload string) any {
		logger.Info("invoke - payload: " + payload)

		ctx := context.Background()
		var envelope ipc.Envelope
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			return nil
		}

		switch envelope.Type {
		case "invoke":
			result, err := invoke.HandleSync(ctx, []byte(payload))
			if err != nil {
				return map[string]string{"error": err.Error()}
			}
			return result
		case "event":
			event.HandleJS(a.window, []byte(payload))
			return nil
		}
		return nil
	})

	return nil
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
