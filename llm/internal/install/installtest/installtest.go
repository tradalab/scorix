// Package installtest builds release archives for tests, shared so the zip and
// tarball shapes the runtimes are tested against are the same everywhere.
package installtest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io/fs"
	"strings"
	"testing"
)

type Entry struct {
	Name, Body, Link string
	Mode             int64
	Dir              bool
}

func Zip(t testing.TB, es ...Entry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range es {
		h := &zip.FileHeader{Name: e.Name, Method: zip.Deflate}
		mode := fs.FileMode(e.Mode).Perm()
		if mode == 0 {
			mode = 0o644
		}
		body := e.Body
		switch {
		case e.Dir:
			h.Name = strings.TrimSuffix(e.Name, "/") + "/"
			mode |= fs.ModeDir
			body = ""
		case e.Link != "":
			// A zip carries a symlink as an entry whose content is the target.
			mode |= fs.ModeSymlink
			body = e.Link
		}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func Tgz(t testing.TB, es ...Entry) []byte {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	w := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.Name, Mode: e.Mode, Size: int64(len(e.Body)), Typeflag: tar.TypeReg}
		switch {
		case e.Dir:
			h.Typeflag, h.Size = tar.TypeDir, 0
		case e.Link != "":
			h.Typeflag, h.Linkname, h.Size = tar.TypeSymlink, e.Link, 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(e.Body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
