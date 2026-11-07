package app

import (
	"testing"
	"testing/fstest"

	"github.com/tradalab/scorix/internal/driver/headless"
	"github.com/tradalab/scorix/window"
)

type recordingDriver struct {
	window.Driver
	got chan window.RuntimeConfig
}

func (d recordingDriver) NewRuntime(cfg window.RuntimeConfig) (window.Runtime, error) {
	d.got <- cfg
	return d.Driver.NewRuntime(cfg)
}

func runtimeConfigFor(t *testing.T, manifest string) window.RuntimeConfig {
	t.Helper()
	d := recordingDriver{Driver: headless.New(), got: make(chan window.RuntimeConfig, 1)}
	old := newDriver
	newDriver = func() window.Driver { return d }
	t.Cleanup(func() { newDriver = old })

	a, err := New(Options{Manifest: []byte(manifest), URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})
	a.OnReady(func(*App) { go a.Quit() })
	if err := a.Run(); err != nil {
		t.Fatal(err)
	}
	return <-d.got
}

func TestWebviewSandboxIsOnUnlessTheManifestTurnsItOff(t *testing.T) {
	base := "app:\n  name: probe\n  identifier: com.example.sandbox\n  version: 1.0.0\n"
	if runtimeConfigFor(t, base).NoWebviewSandbox {
		t.Error("a manifest that says nothing about the sandbox turned it off")
	}
	if !runtimeConfigFor(t, base+"security:\n  webview_sandbox: false\n").NoWebviewSandbox {
		t.Error("security.webview_sandbox: false never reached the driver")
	}
}
