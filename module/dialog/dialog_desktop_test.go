//go:build !android

package dialog

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestWriteFolderFileReplaces(t *testing.T) {
	m := New()
	dir := t.TempDir()
	for _, body := range []string{"first", "second"} {
		if _, err := m.WriteFolderFile(context.Background(), WriteFolderFileRequest{Folder: dir, Name: "backup.json", Data: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(filepath.Join(dir, "backup.json"))
	entries, _ := os.ReadDir(dir)
	if string(got) != "second" || len(entries) != 1 {
		t.Fatalf("got %q in %d files, want one file holding the last write", got, len(entries))
	}
}

func TestWriteFolderFileRefuses(t *testing.T) {
	m := New()
	dir := t.TempDir()
	cases := map[string]WriteFolderFileRequest{
		"invalid_request": {Folder: dir, Name: "../escape.json"},
		CodeFolderGone:    {Folder: filepath.Join(dir, "deleted"), Name: "backup.json"},
	}
	for want, req := range cases {
		_, err := m.WriteFolderFile(context.Background(), req)
		if fault.CodeOf(err) != want {
			t.Errorf("%+v: got %v, want %s", req, err, want)
		}
	}
}

// A leftover tail from a longer earlier write would make the backup unparsable.
func TestRewriteFileReplacesWholeContent(t *testing.T) {
	m := New()
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.json")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"a longer first body", "short"} {
		if _, err := m.RewriteFile(context.Background(), RewriteFileRequest{File: path, Data: []byte(body)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != "short" {
		t.Fatalf("got %q", got)
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode became %v, want the file's own 0644", st.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("%d files left in the folder, want just the one", len(entries))
	}
	_ = os.Remove(path)
	if _, err := m.RewriteFile(context.Background(), RewriteFileRequest{File: path, Data: []byte("x")}); fault.CodeOf(err) != CodeFolderGone {
		t.Fatalf("a deleted file: %v", err)
	}
	if _, err := m.RewriteFile(context.Background(), RewriteFileRequest{File: dir, Data: []byte("x")}); fault.CodeOf(err) != "invalid_request" {
		t.Fatalf("a directory: %v", err)
	}
	if _, err := m.RewriteFile(context.Background(), RewriteFileRequest{File: filepath.Join(path, "gone", "x.json")}); fault.CodeOf(err) != CodeFolderGone {
		t.Fatalf("a file in a missing folder: %v", err)
	}
	if _, err := m.PickFile(context.Background(), PickFileRequest{FileName: "../x.json"}); fault.CodeOf(err) != "invalid_request" {
		t.Fatalf("a file name with a path: %v", err)
	}
}
