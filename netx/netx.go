// Package netx reaches a TCP service the way a desktop client tool has to inside a company network:
// through an optional proxy, an optional SSH bastion, an address rewrite, and an optional TLS layer.
package netx

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DialFunc has the shape client libraries take for a custom dialer (franz-go, go-redis, pgx, net/http).
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Proxy is the first hop. Protocol is socks5 or http (HTTP CONNECT); empty Addr means no proxy.
type Proxy struct {
	Protocol string
	Addr     string
	Username string
	Password string
}

// SSH is the bastion hop, reached through Proxy when one is set. Password and PrivateKey may both be given.
type SSH struct {
	Addr       string
	Username   string
	Password   string
	PrivateKey string
	Passphrase string
	HostKey    HostKeyPolicy
	// KeepAlive is the interval of keepalive@openssh.com probes; zero means 30s, negative turns them off.
	KeepAlive time.Duration
}

// TLS wraps the connection to the target, after the tunnel. An empty ServerName takes the host of the address
// the caller asked for, before any rewrite.
type TLS struct {
	CACert     string
	ClientCert string
	ClientKey  string
	ServerName string
	SkipVerify bool
}

// Config describes one route. Timeout bounds each hop's dial and handshake; zero means 10s.
type Config struct {
	Timeout time.Duration
	// AddrMap rewrites an exact host:port before dialing, for brokers that advertise addresses only they resolve.
	AddrMap map[string]string
	// MapAddr runs after AddrMap for rewrites a table cannot express; it returns its input to leave it alone.
	MapAddr func(addr string) string
	Proxy   *Proxy
	SSH     *SSH
	TLS     *TLS
}

// ErrClosed is returned by Dial on a Route that was closed.
var ErrClosed = errors.New("netx: route closed")

// Route holds the live hops of one Config. Safe for concurrent use; Close it when the connection is dropped.
type Route struct {
	cfg     Config
	timeout time.Duration
	base    contextDialer
	tls     *tls.Config
	tunnel  *tunnel

	mu     sync.Mutex
	closed bool
	http   *http.Transport
}

// Open validates the config and builds the proxy and TLS layers. The SSH session is opened on the first Dial,
// so a host key error surfaces there, as a *HostKeyError.
func Open(cfg Config) (*Route, error) {
	r := &Route{cfg: cfg, timeout: cfg.Timeout}
	if r.timeout <= 0 {
		r.timeout = 10 * time.Second
	}
	base, err := baseDialer(cfg.Proxy, r.timeout)
	if err != nil {
		return nil, err
	}
	r.base = base
	if cfg.TLS != nil {
		if r.tls, err = BuildTLS(*cfg.TLS); err != nil {
			return nil, err
		}
	}
	if cfg.SSH != nil && cfg.SSH.Addr != "" {
		clientCfg, err := buildSSHClientConfig(*cfg.SSH, r.timeout)
		if err != nil {
			return nil, err
		}
		r.tunnel = newTunnel(base, cfg.SSH.Addr, clientCfg, cfg.SSH.KeepAlive, r.timeout)
	}
	return r, nil
}

// Dial runs the whole chain: rewrite, proxy, tunnel, then TLS.
func (r *Route) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := r.DialPlain(ctx, network, r.rewrite(addr))
	if err != nil || r.tls == nil {
		return conn, err
	}
	c := r.tls.Clone()
	if c.ServerName == "" {
		if host, _, err := net.SplitHostPort(addr); err == nil {
			c.ServerName = host
		}
	}
	tc := tls.Client(conn, c)
	hctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if err := tc.HandshakeContext(hctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("netx: tls handshake with %s: %w", addr, err)
	}
	return tc, nil
}

// DialPlain runs proxy and tunnel only: no rewrite, no TLS. It is the hop an HTTP client needs, since
// net/http adds its own TLS.
func (r *Route) DialPlain(ctx context.Context, network, addr string) (net.Conn, error) {
	r.mu.Lock()
	closed := r.closed
	r.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	if r.tunnel != nil {
		return r.tunnel.dial(ctx, network, addr)
	}
	return r.base.DialContext(ctx, network, addr)
}

// HTTPClient sends requests over DialPlain with the system's TLS roots; timeout bounds a whole request.
// Every client of one Route shares one connection pool, which Close releases.
func (r *Route) HTTPClient(timeout time.Duration) *http.Client {
	r.mu.Lock()
	if r.http == nil {
		r.http = &http.Transport{
			DialContext:         r.DialPlain,
			TLSHandshakeTimeout: r.timeout,
			ForceAttemptHTTP2:   true,
			IdleConnTimeout:     90 * time.Second,
			MaxIdleConnsPerHost: 4,
		}
	}
	tr := r.http
	r.mu.Unlock()
	return &http.Client{Timeout: timeout, Transport: tr}
}

// Close ends the SSH session, if any. Calling it again is a no-op; Dial after Close returns ErrClosed.
func (r *Route) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	tr := r.http
	r.mu.Unlock()
	if tr != nil {
		tr.CloseIdleConnections()
	}
	if r.tunnel != nil {
		return r.tunnel.close()
	}
	return nil
}

func (r *Route) rewrite(addr string) string {
	if mapped, ok := r.cfg.AddrMap[addr]; ok {
		addr = mapped
	}
	if r.cfg.MapAddr != nil {
		addr = r.cfg.MapAddr(addr)
	}
	return addr
}

// ParseAddrMap reads one `advertised=reachable` pair per line; blank lines and lines starting with # are skipped.
func ParseAddrMap(s string) (map[string]string, error) {
	out := map[string]string{}
	for i, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("netx: address map line %d: want advertised=reachable, got %q", i+1, line)
		}
		for _, a := range []string{k, v} {
			if _, _, err := net.SplitHostPort(a); err != nil {
				return nil, fmt.Errorf("netx: address map line %d: %q is not host:port", i+1, a)
			}
		}
		out[k] = v
	}
	return out, nil
}
