package webview

import (
	"io"
	"net/http"
)

// SchemeHandler serves a scheme request in-process: the webview resolves
// scorix://app/... by calling h instead of hitting a TCP port.
type SchemeHandler func(req *Request) *Response

type Request struct {
	Method string
	URL    string // e.g. scorix://app/index.html
	Header http.Header
	Body   []byte
}

// Status/Header must be complete enough for the platform webview (content-type,
// and range headers when the backend signals partial content).
type Response struct {
	Status int
	Header http.Header
	Body   io.Reader
}
