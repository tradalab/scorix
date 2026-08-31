package env

import (
	"context"
	"runtime"

	"github.com/tradalab/scorix/module"
)

type EnvModule struct {
	appName    string
	appVersion string
}

func New() *EnvModule { return &EnvModule{} }

func (m *EnvModule) Name() string    { return "env" }
func (m *EnvModule) Version() string { return "1.0.0" }

func (m *EnvModule) Capability() string { return "env" }

func (m *EnvModule) OnLoad(ctx *module.Context) error {
	m.appName = ctx.AppName
	m.appVersion = ctx.AppVersion
	module.Expose(m, "Info", ctx.IPC)
	return nil
}

func (m *EnvModule) OnStart() error  { return nil }
func (m *EnvModule) OnStop() error   { return nil }
func (m *EnvModule) OnUnload() error { return nil }

type Info struct {
	Platform   string `json:"platform"` // windows | darwin | linux
	Arch       string `json:"arch"`
	AppName    string `json:"appName"`
	AppVersion string `json:"appVersion"`
	Locale     string `json:"locale"`   // BCP-47-ish, "" when undetectable
	DarkMode   *bool  `json:"darkMode"` // null when the OS setting is unreadable
}

func (m *EnvModule) Info(context.Context) (*Info, error) {
	dark := osDarkMode()
	return &Info{
		Platform:   runtime.GOOS,
		Arch:       runtime.GOARCH,
		AppName:    m.appName,
		AppVersion: m.appVersion,
		Locale:     osLocale(),
		DarkMode:   dark,
	}, nil
}
