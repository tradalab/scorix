package singleinstance

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
)

var ErrAlreadyRunning = errors.New("singleinstance: another instance is already running")

type Lock struct {
	release func()
}

// payload carries the secondary's argv to the primary, so a protocol or
// file-type launch that hit a running instance still delivers its argument.
func payload() []byte {
	b, err := json.Marshal(os.Args[1:])
	if err != nil {
		return []byte("[]")
	}
	return append(b, '\n')
}

func parsePayload(b []byte) []string {
	var args []string
	if json.Unmarshal(b, &args) != nil {
		return nil
	}
	return args
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
