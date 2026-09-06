package logger

import (
	"sync"

	"go.uber.org/zap/zapcore"
)

const defaultRing = 200

// ring keeps the last lines in memory so a crash report can say what the app was
// doing. Not a log file: the point is that it is already there when one happens.
type ring struct {
	mu   sync.Mutex
	buf  []string
	next int
	full bool
}

var logRing = &ring{buf: make([]string, defaultRing)}

func (r *ring) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = line
	r.next++
	if r.next == len(r.buf) {
		r.next, r.full = 0, true
	}
}

// Tail returns the buffered lines, oldest first.
func Tail() []string {
	r := logRing
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]string{}, r.buf[:r.next]...)
	}
	out := make([]string, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	return append(out, r.buf[:r.next]...)
}

// SetRingSize resizes the buffer and drops what it held; 0 disables capture.
func SetRingSize(n int) {
	if n < 0 {
		n = 0
	}
	logRing.mu.Lock()
	defer logRing.mu.Unlock()
	logRing.buf, logRing.next, logRing.full = make([]string, n), 0, false
}

// A tee, so turning capture on never changes what the real sink receives.
type ringWriter struct{ zapcore.WriteSyncer }

func (w ringWriter) Write(p []byte) (int, error) {
	if len(logRing.buf) > 0 {
		logRing.add(trimNewline(string(p)))
	}
	return w.WriteSyncer.Write(p)
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
