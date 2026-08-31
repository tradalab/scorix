package httpfetch

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/module"
)

const (
	maxBodyBytes   = 10 << 20
	maxTimeout     = 60 * time.Second
	defaultTimeout = 15 * time.Second
)

type Config struct {
	Allow []string `json:"allow"`
}

type HTTPModule struct {
	cfg    Config
	client *http.Client
}

func New() *HTTPModule { return &HTTPModule{} }

func (m *HTTPModule) Name() string    { return "http" }
func (m *HTTPModule) Version() string { return "1.0.0" }

func (m *HTTPModule) Capability() string { return "http" }

func (m *HTTPModule) OnLoad(ctx *module.Context) error {
	if err := ctx.Decode(&m.cfg); err != nil {
		return fmt.Errorf("decode config: %w", err)
	}
	m.client = &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error { // an allowed host 302ing elsewhere must not become a hole
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if !m.hostAllowed(req.URL.Host) {
				return fault.Errorf(fault.CodeDenied, "redirect to %q is outside modules.http.allow", req.URL.Host)
			}
			return nil
		},
	}
	module.Expose(m, "Fetch", ctx.IPC)
	return nil
}

func (m *HTTPModule) OnStart() error  { return nil }
func (m *HTTPModule) OnStop() error   { return nil }
func (m *HTTPModule) OnUnload() error { return nil }

func (m *HTTPModule) hostAllowed(host string) bool {
	h := strings.ToLower(host)
	bare := h
	if hp, _, err := net.SplitHostPort(h); err == nil {
		bare = hp
	}
	for _, a := range m.cfg.Allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "*.") {
			if strings.HasSuffix(bare, a[1:]) || bare == a[2:] {
				return true
			}
			continue
		}
		if h == a || bare == a {
			return true
		}
	}
	return false
}

type FetchRequest struct {
	URL       string            `json:"url"`
	Method    string            `json:"method"` // default GET
	Headers   map[string]string `json:"headers"`
	Body      string            `json:"body"` // sent as-is (text)
	TimeoutMs int               `json:"timeoutMs"`
}

type FetchResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	Base64  bool              `json:"base64"`
}

func (m *HTTPModule) Fetch(ctx context.Context, req FetchRequest) (*FetchResponse, error) {
	if req.URL == "" {
		return nil, fault.New("invalid_request", "url is empty")
	}
	if !strings.HasPrefix(strings.ToLower(req.URL), "http://") && !strings.HasPrefix(strings.ToLower(req.URL), "https://") {
		return nil, fault.New("invalid_request", "only http(s) URLs are allowed")
	}
	method := strings.ToUpper(req.Method)
	if method == "" {
		method = http.MethodGet
	}

	timeout := defaultTimeout
	if req.TimeoutMs > 0 {
		timeout = min(time.Duration(req.TimeoutMs)*time.Millisecond, maxTimeout)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var body io.Reader
	if req.Body != "" {
		body = strings.NewReader(req.Body)
	}
	hr, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, fault.Wrap("invalid_request", err)
	}
	if !m.hostAllowed(hr.URL.Host) {
		return nil, fault.Errorf(fault.CodeDenied, "host %q is outside modules.http.allow", hr.URL.Host).
			With("host", hr.URL.Host)
	}
	for k, v := range req.Headers {
		hr.Header.Set(k, v)
	}

	resp, err := m.client.Do(hr)
	if err != nil {
		return nil, fault.Wrap(fault.CodeUnavailable, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, fault.Wrap(fault.CodeUnavailable, err)
	}
	if len(data) > maxBodyBytes {
		return nil, fault.Errorf("overloaded", "response body exceeds %d bytes", maxBodyBytes)
	}

	out := &FetchResponse{Status: resp.StatusCode, Headers: map[string]string{}}
	for k := range resp.Header {
		out.Headers[k] = resp.Header.Get(k)
	}
	ct := resp.Header.Get("Content-Type")
	if isTextual(ct) {
		out.Body = string(data)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(data)
		out.Base64 = true
	}
	return out, nil
}

func isTextual(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.HasPrefix(ct, "text/") ||
		strings.Contains(ct, "json") ||
		strings.Contains(ct, "xml") ||
		strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "x-www-form-urlencoded")
}
