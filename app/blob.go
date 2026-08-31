package app

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"

	"github.com/tradalab/scorix/webview"
)

const blobPathPrefix = "/__scorix/blob/"

type Blob struct {
	a  *App
	id string
	ct string
}

func (b *Blob) URL() string { return blobPathPrefix + b.id }

func (b *Blob) Close() {
	b.a.mu.Lock()
	delete(b.a.blobs, b.id)
	b.a.mu.Unlock()
}

type blobEntry struct {
	ct   string
	data []byte
}

func (a *App) ServeBlob(contentType string, data []byte) *Blob {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	id := hex.EncodeToString(raw)
	a.mu.Lock()
	if a.blobs == nil {
		a.blobs = map[string]blobEntry{}
	}
	a.blobs[id] = blobEntry{ct: contentType, data: data}
	a.mu.Unlock()
	return &Blob{a: a, id: id, ct: contentType}
}

func (a *App) blobFor(urlPath string) (blobEntry, bool) {
	id := strings.TrimPrefix(urlPath, blobPathPrefix)
	a.mu.Lock()
	e, ok := a.blobs[id]
	a.mu.Unlock()
	return e, ok
}

func (a *App) schemeWithBlobs(h webview.SchemeHandler) webview.SchemeHandler {
	assets := a.withCSP(h)
	return func(req *webview.Request) *webview.Response {
		if p := schemeURLPath(req.URL); strings.HasPrefix(p, blobPathPrefix) {
			e, ok := a.blobFor(p)
			if !ok {
				return &webview.Response{Status: http.StatusNotFound, Header: http.Header{}, Body: strings.NewReader("gone")}
			}
			hd := http.Header{}
			if e.ct != "" {
				hd.Set("Content-Type", e.ct)
			}
			return &webview.Response{Status: http.StatusOK, Header: hd, Body: bytes.NewReader(e.data)}
		}
		return assets(req)
	}
}

func schemeURLPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "/"
	}
	return u.Path
}
