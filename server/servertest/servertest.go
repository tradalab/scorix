// Package servertest runs a scorix service inside a test: a real server.Server on an
// in-memory listener, with no port, reached by callers that each speak as a chosen principal.
package servertest

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/server"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
)

const principalHeader = "Scorix-Test-Principal"

// Auth trusts the principal a Service.Dial caller names and refuses a call that names none
// as unauthenticated. It is for the Mux a test builds, never for a running service.
var Auth kernel.Authenticator = kernel.AuthenticatorFunc(func(ctx context.Context, call kernel.Call) (context.Context, error) {
	kind, id, ok := strings.Cut(call.Header.Get(principalHeader), ":")
	if !ok || kind == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("servertest: the call names no principal"))
	}
	return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.Kind(kind), ID: id}), nil
})

type Service struct {
	// URL is the base every path of the service hangs off; only HTTPClient can reach it.
	URL    string
	client *http.Client
}

// Start runs a server built from opt and mount until t ends; Run's error fails t.
func Start(t testing.TB, opt server.Options, mount func(*server.Server)) *Service {
	t.Helper()
	ln := &pipes{conns: make(chan net.Conn), done: make(chan struct{})}
	s := server.New(opt)
	mount(s)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- s.Run(ctx, ln) }()
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	tr := &http.Transport{Protocols: &p, DialContext: ln.dial}
	t.Cleanup(func() {
		cancel()
		if err := <-stopped; err != nil {
			t.Errorf("servertest: the server stopped with %v", err)
		}
		ln.Close()
		tr.CloseIdleConnections()
	})
	return &Service{URL: "http://servertest.invalid", client: &http.Client{Transport: tr}}
}

// HTTPClient reaches the service in memory, for REST, MCP or any plain HTTP call.
func (s *Service) HTTPClient() *http.Client { return s.client }

// Dial returns a Conn whose calls run as p under Auth; the zero Principal names nobody.
// opt.HTTPClient is replaced and opt.Header still runs after the principal is set.
func (s *Service) Dial(p kernel.Principal, opt scorixconnect.ConnOptions) *scorixconnect.Conn {
	header := opt.Header
	opt.HTTPClient = s.client
	opt.Header = func(ctx context.Context, h http.Header) {
		h.Set(principalHeader, string(p.Kind)+":"+p.ID)
		if header != nil {
			header(ctx, h)
		}
	}
	return scorixconnect.Dial(s.URL, opt)
}

type pipes struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

type memAddr struct{}

func (memAddr) Network() string { return "memory" }
func (memAddr) String() string  { return "servertest" }

func (l *pipes) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *pipes) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *pipes) Addr() net.Addr { return memAddr{} }

func (l *pipes) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.done:
		client.Close()
		server.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		client.Close()
		server.Close()
		return nil, ctx.Err()
	}
}
