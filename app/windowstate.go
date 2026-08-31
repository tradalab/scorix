package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/tradalab/scorix/module"
	"github.com/tradalab/scorix/window"
)

type windowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	W         int  `json:"w"`
	H         int  `json:"h"`
	Maximized bool `json:"maximized"`
}

var stateKeyClean = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func (a *App) windowStatePath(key string) string {
	name := a.cfg.App.Name
	if name == "" {
		name = a.opts.Identifier
	}
	file := "window-state.json" // the main window keeps the pre-keyed filename
	if key != "main" {
		file = "window-state-" + stateKeyClean.ReplaceAllString(key, "-") + ".json"
	}
	return filepath.Join(module.DataDir(name), file)
}

func (a *App) loadWindowState(key string) (windowState, bool) {
	b, err := os.ReadFile(a.windowStatePath(key))
	if err != nil {
		return windowState{}, false
	}
	var st windowState
	if json.Unmarshal(b, &st) != nil || st.W <= 0 || st.H <= 0 {
		return windowState{}, false
	}
	return st, true
}

func (a *App) saveWindowState(key string, aw *AppWindow) {
	st, had := a.loadWindowState(key)
	switch aw.State() { // maximized/minimized must keep the last NORMAL rect, the classic remember-state bug
	case window.StateNormal:
		st.X, st.Y = aw.Position()
		st.W, st.H = aw.Size()
		st.Maximized = false
	case window.StateMaximized:
		if !had {
			st.W, st.H = a.opts.Width, a.opts.Height
		}
		st.Maximized = true
	default: // minimized/fullscreen: keep whatever was stored
		if !had {
			return
		}
	}
	if st.W <= 0 || st.H <= 0 {
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	path := a.windowStatePath(key)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o644)
}

func (a *App) hookWindowState(rt window.Runtime, aw *AppWindow, key string) {
	var closed bool
	aw.On(window.EventClose, func(window.EventData) {
		a.saveWindowState(key, aw)
		closed = true
	})
	rt.On(window.RuntimeBeforeQuit, func() {
		if !closed {
			a.saveWindowState(key, aw)
		}
	})
}

func screensOf(rt window.Runtime) []window.Screen {
	if sl, ok := rt.(window.ScreenLister); ok {
		return sl.Screens()
	}
	return nil
}

func stateOnScreen(st windowState, screens []window.Screen) bool {
	if len(screens) == 0 {
		return true
	}
	const margin = 40
	for _, s := range screens {
		if st.X+margin >= s.X && st.X+margin <= s.X+s.W &&
			st.Y+margin >= s.Y && st.Y+margin <= s.Y+s.H {
			return true
		}
	}
	return false
}

func (a *App) Screens() []window.Screen {
	a.mu.Lock()
	rt := a.rt
	a.mu.Unlock()
	if rt == nil {
		return nil
	}
	if sl, ok := rt.(window.ScreenLister); ok {
		return sl.Screens()
	}
	return nil
}
