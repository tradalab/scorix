package app

import (
	"context"
	"encoding/json"
	"os"

	"github.com/tradalab/scorix/internal/deeplink"
	ipc "github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/logger"
)

// launchArgs is a hook so tests can fake a protocol/file-type launch.
var launchArgs = func() []string { return os.Args[1:] }

// OnOpenURL registers fn for launches of a declared app.protocols scheme -
// both the process's own launch args and ones forwarded by a second instance.
// Register before Run; fn runs off the UI thread.
func (a *App) OnOpenURL(fn func(url string)) {
	a.mu.Lock()
	a.openURLFns = append(a.openURLFns, fn)
	a.mu.Unlock()
}

// OnOpenFile is OnOpenURL for declared app.file_types paths.
func (a *App) OnOpenFile(fn func(path string)) {
	a.mu.Lock()
	a.openFileFns = append(a.openFileFns, fn)
	a.mu.Unlock()
}

// OnFileDrop registers fn for OS file drops on windows with FileDrop enabled
// (window.file_drop / window.Options.FileDrop). fn runs on the UI thread.
func (a *App) OnFileDrop(fn func(w *AppWindow, paths []string)) {
	a.mu.Lock()
	a.fileDropFns = append(a.fileDropFns, fn)
	a.mu.Unlock()
}

// registerOSHandlers claims the declared schemes/types with the OS. Every Run:
// HKCU rewriting keeps the handler pointing at the binary that just started,
// across updates and dev builds.
func (a *App) registerOSHandlers() {
	if len(a.cfg.App.Protocols) == 0 && len(a.cfg.App.FileTypes) == 0 {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		logger.Warn("app: cannot resolve executable for protocol registration", "err", err)
		return
	}
	if err := deeplink.Register(a.opts.Identifier, a.cfg.App.Name, exe, a.cfg.App.Protocols, a.cfg.App.FileTypes); err != nil {
		logger.Warn("app: protocol/file-type registration failed", "err", err)
	}
}

// dispatchOpenArgs classifies argv and delivers to Go handlers plus the
// frontend ("sys:open-url"/"sys:open-file"). The frontend may not be loaded
// yet on a cold start - that is what the sys:launch command is for.
func (a *App) dispatchOpenArgs(args []string) {
	urls, files := deeplink.Classify(args, a.cfg.App.Protocols, a.cfg.App.FileTypes)
	if len(urls) == 0 && len(files) == 0 {
		return
	}
	a.mu.Lock()
	a.launchURLs = append(a.launchURLs, urls...)
	a.launchFiles = append(a.launchFiles, files...)
	urlFns := append([]func(string){}, a.openURLFns...)
	fileFns := append([]func(string){}, a.openFileFns...)
	a.mu.Unlock()

	for _, u := range urls {
		for _, fn := range urlFns {
			fn(u)
		}
		a.Emit("sys:open-url", map[string]string{"url": u})
	}
	for _, f := range files {
		for _, fn := range fileFns {
			fn(f)
		}
		a.Emit("sys:open-file", map[string]string{"path": f})
	}
}

// registerLaunchCommand exposes everything this process was asked to open, so
// a frontend that finished loading after the events were emitted can pull them.
func (a *App) registerLaunchCommand() {
	a.reg.Command("sys:launch", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		a.mu.Lock()
		defer a.mu.Unlock()
		return map[string][]string{
			"urls":  append([]string{}, a.launchURLs...),
			"files": append([]string{}, a.launchFiles...),
		}, nil
	})
}
