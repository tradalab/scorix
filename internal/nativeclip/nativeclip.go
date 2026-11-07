// Package nativeclip is the text clipboard a platform driver installs when the toolkit it runs owns one.
// The clipboard module asks it first, so it works where no external clipboard tool is installed.
package nativeclip

import "sync"

// Text reads and writes the system clipboard as plain text. It is called from any goroutine.
type Text interface {
	ReadText() (string, error)
	WriteText(text string) error
}

var (
	mu        sync.RWMutex
	installed Text
)

// Install makes t the clipboard every later Installed call returns; nil removes it.
func Install(t Text) {
	mu.Lock()
	defer mu.Unlock()
	installed = t
}

// Installed returns the clipboard the driver installed, or nil when there is none.
func Installed() Text {
	mu.RLock()
	defer mu.RUnlock()
	return installed
}
