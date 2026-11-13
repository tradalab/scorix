//go:build linux && !android

package webkitgtk

import (
	"reflect"
	"testing"
)

func TestOnlyLocalFileURIsBecomeDroppedPaths(t *testing.T) {
	got := filePathsFromURIs([]string{
		"file:///home/me/report.pdf",
		"  file:///home/me/with%20space.txt\r",
		"# a comment line of text/uri-list",
		"",
		"https://example.test/page.html",
		"file://otherhost/share/x.bin",
		"file://localhost/tmp/a.log",
		"FILE:///tmp/upper.txt",
		"file:///home/me/report.pdf",
		"file:",
		"%zz",
	})
	want := []string{"/home/me/report.pdf", "/home/me/with space.txt", "/tmp/a.log", "/tmp/upper.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestADropWithNoLocalFilesGivesNoPaths(t *testing.T) {
	if got := filePathsFromURIs([]string{"https://example.test/", "# only a comment"}); len(got) != 0 {
		t.Fatalf("paths = %q, want none", got)
	}
}

func TestOnlyAFileManagerDragIsTakenAsAFileDrop(t *testing.T) {
	cases := []struct {
		name    string
		targets []string
		want    bool
	}{
		{"nautilus or cosmic-files", []string{"x-special/gnome-copied-files", "text/uri-list", "UTF8_STRING"}, true},
		{"bare uri list", []string{"text/uri-list"}, true},
		{"firefox link", []string{"text/x-moz-url", "_NETSCAPE_URL", "text/uri-list", "text/plain"}, false},
		{"chromium link", []string{"text/uri-list", "text/plain", "_NETSCAPE_URL"}, false},
		{"plain text", []string{"text/plain", "UTF8_STRING"}, false},
		{"nothing", nil, false},
	}
	for _, c := range cases {
		if got := isFileDragTargets(c.targets); got != c.want {
			t.Errorf("%s: isFileDragTargets = %v, want %v", c.name, got, c.want)
		}
	}
}
