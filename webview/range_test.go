package webview

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func ranged(spec string, status int) *Response {
	req := &Request{Header: http.Header{}}
	if spec != "" {
		req.Header.Set("Range", spec)
	}
	return ServeRange(req, &Response{Status: status, Header: http.Header{"Content-Type": {"audio/wav"}}, Body: strings.NewReader("0123456789")})
}

func body(t *testing.T, r *Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestARangeOnAWholeResponseAnswersWithItsSliceAndWhereItSits(t *testing.T) {
	for _, c := range []struct{ spec, want, contentRange string }{
		{"bytes=2-4", "234", "bytes 2-4/10"},
		{"bytes=7-", "789", "bytes 7-9/10"},
		{"bytes=8-99", "89", "bytes 8-9/10"},
		{"bytes=-3", "789", "bytes 7-9/10"},
		{"bytes=-30", "0123456789", "bytes 0-9/10"},
	} {
		r := ranged(c.spec, http.StatusOK)
		if r.Status != http.StatusPartialContent || r.Header.Get("Content-Range") != c.contentRange || r.Header.Get("Content-Type") != "audio/wav" {
			t.Fatalf("%s: %d %v", c.spec, r.Status, r.Header)
		}
		if got := body(t, r); got != c.want || r.Header.Get("Content-Length") != strconvLen(c.want) {
			t.Fatalf("%s: body %q length %s", c.spec, got, r.Header.Get("Content-Length"))
		}
	}
}

func strconvLen(s string) string { return strconv.Itoa(len(s)) }

func TestARangePastTheEndIsUnsatisfiable(t *testing.T) {
	r := ranged("bytes=10-", http.StatusOK)
	if r.Status != http.StatusRequestedRangeNotSatisfiable || r.Header.Get("Content-Range") != "bytes */10" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestWithoutAUsableRangeTheWholeBodyGoesOutAndSaysItCanSeek(t *testing.T) {
	for _, spec := range []string{"", "bytes=0-1,4-5", "items=0-1", "bytes=5-2", "bytes=x-"} {
		r := ranged(spec, http.StatusOK)
		if r.Status != http.StatusOK || body(t, r) != "0123456789" || r.Header.Get("Accept-Ranges") != "bytes" {
			t.Fatalf("%q: %d %v", spec, r.Status, r.Header)
		}
	}
}

func TestAnErrorResponseIsNeverSliced(t *testing.T) {
	r := ranged("bytes=0-1", http.StatusNotFound)
	if r.Status != http.StatusNotFound || body(t, r) != "0123456789" || r.Header.Get("Accept-Ranges") != "" {
		t.Fatalf("%d %v", r.Status, r.Header)
	}
}

func TestServeRangeLeavesTheHandlersResponseAndHeadersAsTheyWere(t *testing.T) {
	shared := http.Header{"Content-Type": {"audio/wav"}}
	in := &Response{Status: http.StatusOK, Header: shared, Body: strings.NewReader("0123456789")}
	req := &Request{Header: http.Header{"Range": {"bytes=0-1"}}}
	if got := ServeRange(req, in); got == in || got.Status != http.StatusPartialContent {
		t.Fatalf("ServeRange answered with the handler's own response: %+v", got)
	}
	if len(shared) != 1 || in.Status != http.StatusOK {
		t.Fatalf("the handler's response changed under it: %d %v", in.Status, shared)
	}
}
