package webview

import (
	"bytes"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// SchemeFromFS serves files from fsys (URL host is a namespace, not path).
// Extensionless misses fall back to index.html (SPA); other misses 404.
func SchemeFromFS(fsys fs.FS) SchemeHandler {
	return func(req *Request) *Response {
		name := assetPath(req.URL)

		if f, err := fsys.Open(name); err == nil {
			return fileResponse(name, f)
		}

		if path.Ext(name) == "" {
			if idx, err := fsys.Open("index.html"); err == nil {
				return fileResponse("index.html", idx)
			}
		}
		return notFound()
	}
}

func fileResponse(name string, f fs.File) *Response {
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return notFound()
	}
	h := http.Header{}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		h.Set("Content-Type", ct)
	}
	return &Response{Status: http.StatusOK, Header: h, Body: bytes.NewReader(data)}
}

func notFound() *Response {
	return &Response{
		Status: http.StatusNotFound,
		Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:   strings.NewReader("404 not found"),
	}
}

// assetPath maps scorix://app/foo/bar.js -> foo/bar.js. Cleans so the result
// never escapes fsys; root/dir and traversal resolve to index.html.
func assetPath(rawurl string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "index.html"
	}
	p := strings.TrimPrefix(u.Path, "/")
	if p == "" || strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	p = path.Clean(p)
	if p == "." || strings.HasPrefix(p, "..") {
		return "index.html"
	}
	return p
}
