package singleinstance

import (
	"errors"
	"regexp"
)

var ErrAlreadyRunning = errors.New("singleinstance: another instance is already running")

type Lock struct {
	release func()
}

func (l *Lock) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func sanitize(id string) string {
	if id == "" {
		id = "scorix-app"
	}
	return unsafeName.ReplaceAllString(id, "_")
}
