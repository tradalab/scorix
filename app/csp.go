package app

import (
	"bytes"
	"io"
	"strings"

	"github.com/tradalab/scorix/webview"
)

func (a *App) withCSP(h webview.SchemeHandler) webview.SchemeHandler {
	csp := cspValue(a.cfg.Security.CSP)
	if csp == "" {
		return h
	}
	return func(req *webview.Request) *webview.Response {
		resp := h(req)
		if resp == nil || resp.Header == nil ||
			!strings.Contains(resp.Header.Get("Content-Type"), "html") {
			return resp
		}
		resp.Header.Set("Content-Security-Policy", csp)
		if resp.Body != nil {
			data, _ := io.ReadAll(resp.Body)
			resp.Body = bytes.NewReader(injectCSPMeta(data, csp))
		}
		return resp
	}
}

var cspAttrEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;")

func injectCSPMeta(html []byte, csp string) []byte {
	tag := []byte(`<meta http-equiv="Content-Security-Policy" content="` + cspAttrEscaper.Replace(csp) + `">`)
	if at := headOpenEnd(html); at >= 0 {
		out := make([]byte, 0, len(html)+len(tag))
		out = append(out, html[:at]...)
		out = append(out, tag...)
		out = append(out, html[at:]...)
		return out
	}
	return append(tag, html...)
}

func headOpenEnd(html []byte) int {
	lower := bytes.ToLower(html)
	from := 0
	for {
		i := bytes.Index(lower[from:], []byte("<head"))
		if i < 0 {
			return -1
		}
		i += from
		rest := lower[i+len("<head"):]
		if len(rest) > 0 && (rest[0] == '>' || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r') {
			if j := bytes.IndexByte(rest, '>'); j >= 0 {
				return i + len("<head") + j + 1
			}
			return -1
		}
		from = i + len("<head")
	}
}
