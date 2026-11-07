package netx

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
)

type contextDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
}

func baseDialer(p *Proxy, timeout time.Duration) (contextDialer, error) {
	plain := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if p == nil || p.Addr == "" {
		return plain, nil
	}
	if _, _, err := net.SplitHostPort(p.Addr); err != nil {
		return nil, fmt.Errorf("netx: proxy address %q is not host:port", p.Addr)
	}
	switch strings.ToLower(strings.TrimSpace(p.Protocol)) {
	case "socks5":
		var auth *proxy.Auth
		if p.Username != "" {
			auth = &proxy.Auth{User: p.Username, Password: p.Password}
		}
		d, err := proxy.SOCKS5("tcp", p.Addr, auth, plain)
		if err != nil {
			return nil, fmt.Errorf("netx: socks5 proxy: %w", err)
		}
		cd, ok := d.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("netx: socks5 dialer ignores context")
		}
		return cd, nil
	case "http", "connect":
		return &httpConnect{addr: p.Addr, username: p.Username, password: p.Password, timeout: timeout}, nil
	default:
		return nil, fmt.Errorf("netx: unknown proxy protocol %q: want socks5 or http (a TLS proxy is not supported)", p.Protocol)
	}
}

type httpConnect struct {
	addr     string
	username string
	password string
	timeout  time.Duration
}

func (d *httpConnect) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: d.timeout}).DialContext(ctx, "tcp", d.addr)
	if err != nil {
		return nil, fmt.Errorf("netx: http proxy %s: %w", d.addr, err)
	}
	deadline := time.Now().Add(d.timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if d.username != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(d.username + ":" + d.password))
		req.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("netx: http proxy %s: %w", d.addr, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("netx: http proxy %s: %w", d.addr, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("netx: http proxy %s refused CONNECT to %s: %s", d.addr, addr, resp.Status)
	}
	if !stop() {
		return nil, fmt.Errorf("netx: http proxy %s: %w", d.addr, ctx.Err())
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }
