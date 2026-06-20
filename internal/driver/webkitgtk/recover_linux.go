//go:build linux

package webkitgtk

import "github.com/tradalab/scorix/logger"

// recoverCB contains a panic crossing the GTK/glib (purego) callback boundary,
// where unwinding through C is undefined behavior. Every callback that runs app
// code must defer it; the callback then returns its zero value.
func recoverCB(where string) {
	if r := recover(); r != nil {
		logger.Error("webkitgtk: recovered panic in C callback", "where", where, "panic", r)
	}
}
