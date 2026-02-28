package sandbox

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
)

// SecurityMiddleware injects security features into HTML responses
func SecurityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip non-HTML requests
		if !isHTMLRequest(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Capture response
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		// Copy headers and body
		result := rec.Result()
		defer result.Body.Close()

		bodyBytes, err := io.ReadAll(result.Body)
		if err != nil {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		bodyStr := string(bodyBytes)
		if bodyStr == "" {
			copyHeaders(w, rec.Header())
			w.WriteHeader(result.StatusCode)
			return
		}

		// Inject security content
		modifiedHTML := injectSecurity(bodyStr)

		// Copy headers and update CSP
		copyHeaders(w, rec.Header())
		if cfg.CSP != "none" {
			w.Header().Set("Content-Security-Policy", buildCSP())
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Length", strconv.Itoa(len(modifiedHTML)))

		w.WriteHeader(result.StatusCode)
		_, _ = w.Write([]byte(modifiedHTML))
	})
}

// copyHeaders copies all headers from src to dst
func copyHeaders(dst http.ResponseWriter, src http.Header) {
	for k, values := range src {
		for _, v := range values {
			dst.Header().Add(k, v)
		}
	}
}

// isHTMLRequest checks if the request is likely for HTML
func isHTMLRequest(r *http.Request) bool {
	path := r.URL.Path
	return path == "/" ||
		strings.HasSuffix(path, ".html") ||
		strings.HasSuffix(path, ".htm") ||
		strings.Contains(r.Header.Get("Accept"), "text/html")
}

// injectSecurity adds JS + optional CSP meta
func injectSecurity(html string) string {
	if !cfg.AllowRightClick {
		html = injectDisableRightClick(html)
	}
	if cfg.CSP != "none" {
		cspMeta := `<meta http-equiv="Content-Security-Policy" content="` + buildCSP() + `">`
		html = injectIntoHead(html, cspMeta)
	}
	return html
}

// injectDisableRightClick adds JS to block right-click
func injectDisableRightClick(html string) string {
	js := `<script>document.addEventListener('contextmenu',function(e){e.preventDefault();},false);</script>`
	return injectIntoHead(html, js)
}

// injectIntoHead safely inserts content into <head> or before <body>
func injectIntoHead(html, content string) string {
	lower := strings.ToLower(html)

	// Case 1: Before </head>
	if idx := strings.Index(lower, "</head>"); idx != -1 {
		return html[:idx] + content + html[idx:]
	}

	// Case 2: After <body ...>
	if idx := strings.Index(lower, "<body"); idx != -1 {
		bodyTag := html[idx:]
		if closeIdx := strings.Index(bodyTag, ">"); closeIdx != -1 {
			insertPos := idx + closeIdx + 1
			return html[:insertPos] + content + html[insertPos:]
		}
	}

	// Case 3: Prepend
	return content + html
}

// buildCSP generates secure CSP
func buildCSP() string {
	csp := []string{
		"default-src 'self'",
		"script-src 'self' 'unsafe-inline'",
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data: blob:",
		"font-src 'self' data:",
		"media-src 'self' data:",
		"object-src 'none'",
		"frame-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
	}

	if cfg.Allowlist.HTTP {
		csp = append(csp, "connect-src *")
	}
	if cfg.Allowlist.Clipboard {
		csp = append(csp, "clipboard-write")
	}
	if cfg.Allowlist.Notification {
		csp = append(csp, "notifications")
	}

	return strings.Join(csp, "; ") + ";"
}
