//go:build darwin

package wkwebview

import "github.com/tradalab/scorix/logger"

// recoverCB contains a panic crossing the Objective-C callback boundary, where
// unwinding through the ObjC runtime is undefined behavior. Every callback that
// runs app code must defer it; the callback then returns its zero value.
func recoverCB(where string) {
	if r := recover(); r != nil {
		logger.Error("wkwebview: recovered panic in C callback", "where", where, "panic", r)
	}
}
