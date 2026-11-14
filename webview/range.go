package webview

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ServeRange answers a single "bytes=" Range on a 200 response with 206 and its slice, or 416
// when the range lies past the end. Other responses and multi-range requests pass through whole,
// each 200 advertising Accept-Ranges so a media player knows it may seek.
func ServeRange(req *Request, resp *Response) *Response {
	if resp == nil || resp.Status != http.StatusOK && resp.Status != 0 {
		return resp
	}
	out := &Response{Status: resp.Status, Header: resp.Header.Clone(), Body: resp.Body}
	if out.Header == nil {
		out.Header = http.Header{}
	}
	out.Header.Set("Accept-Ranges", "bytes")
	spec := ""
	if req != nil && req.Header != nil {
		spec = req.Header.Get("Range")
	}
	if spec == "" {
		return out
	}
	var data []byte
	if out.Body != nil {
		b, err := io.ReadAll(out.Body)
		if err != nil {
			return &Response{Status: http.StatusInternalServerError, Header: http.Header{}, Body: strings.NewReader("read failed")}
		}
		data = b
	}
	out.Body = bytes.NewReader(data)
	size := int64(len(data))
	start, end, ok := byteRange(spec, size)
	if !ok {
		return out
	}
	if start >= size {
		h := http.Header{"Content-Range": {"bytes */" + strconv.FormatInt(size, 10)}}
		return &Response{Status: http.StatusRequestedRangeNotSatisfiable, Header: h, Body: strings.NewReader("")}
	}
	out.Status = http.StatusPartialContent
	out.Header.Set("Content-Range", "bytes "+strconv.FormatInt(start, 10)+"-"+strconv.FormatInt(end, 10)+"/"+strconv.FormatInt(size, 10))
	out.Header.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	out.Body = bytes.NewReader(data[start : end+1])
	return out
}

func byteRange(spec string, size int64) (start, end int64, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(spec), "bytes=")
	if !found || strings.Contains(rest, ",") {
		return 0, 0, false
	}
	first, last, found := strings.Cut(strings.TrimSpace(rest), "-")
	if !found {
		return 0, 0, false
	}
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	end = size - 1
	if last != "" {
		e, err := strconv.ParseInt(last, 10, 64)
		if err != nil || e < start {
			return 0, 0, false
		}
		if e < end {
			end = e
		}
	}
	return start, end, true
}
