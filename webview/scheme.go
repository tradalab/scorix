package webview

import (
	"io"
	"net/http"
)

// SchemeHandler serves scorix://app/... in-process, no TCP port.
type SchemeHandler func(req *Request) *Response

type Request struct {
	Method string
	URL    string // e.g. scorix://app/index.html
	Header http.Header
	Body   []byte
}

// Header must set content-type (and range headers for partial content) for the
// platform webview.
type Response struct {
	Status int
	Header http.Header
	Body   io.Reader
}
