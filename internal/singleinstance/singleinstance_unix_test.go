//go:build !windows

package singleinstance

import (
	"strings"
	"testing"
)

func TestSockPathFitsSunPath(t *testing.T) {
	long := sanitize("com.tradalab." + strings.Repeat("longidentifier.", 8))
	p := sockPath(long)
	if len(p) > maxSockPath {
		t.Fatalf("sockPath len = %d, want <= %d: %s", len(p), maxSockPath, p)
	}
	if p == sockPath(long+"x") {
		t.Fatal("two identifiers collapsed onto one socket")
	}
	if short := sockPath("com.tradalab.pchub"); !strings.Contains(short, "com.tradalab.pchub") {
		t.Fatalf("a name that fits must stay readable: %s", short)
	}
}
