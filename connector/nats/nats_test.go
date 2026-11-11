package nats

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

type fake struct {
	net.Listener
	first   chan byte
	connect chan string
	sni     chan string
	gone    chan struct{}
	pong    bool
	delay   time.Duration
}

func serve(t *testing.T, pong bool) *fake {
	t.Helper()
	return serveAfter(t, pong, 0)
}

func serveAfter(t *testing.T, pong bool, delay time.Duration) *fake {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fake{Listener: ln, first: make(chan byte, 16), connect: make(chan string, 16), sni: make(chan string, 16), gone: make(chan struct{}, 16), pong: pong, delay: delay}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.talk(conn)
		}
	}()
	return f
}

func (f *fake) url() string { return "nats://" + f.Addr().String() }

func (f *fake) talk(conn net.Conn) {
	defer conn.Close()
	time.Sleep(f.delay)
	conn.Write([]byte(`INFO {"server_id":"fake","version":"2.15.2","proto":1,"max_payload":1048576,"tls_available":true}` + "\r\n"))
	r := bufio.NewReader(conn)
	b, err := r.Peek(1)
	if err != nil {
		return
	}
	f.first <- b[0]
	if b[0] == 0x16 {
		tls.Server(&buffered{Conn: conn, r: r}, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			f.sni <- h.ServerName
			return nil, errors.New("no certificate here")
		}}).Handshake()
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			f.gone <- struct{}{}
			return
		}
		switch {
		case strings.HasPrefix(line, "CONNECT "):
			f.connect <- strings.TrimSpace(strings.TrimPrefix(line, "CONNECT "))
		case strings.HasPrefix(line, "PING") && f.pong:
			conn.Write([]byte("PONG\r\n"))
		}
	}
}

type buffered struct {
	net.Conn
	r *bufio.Reader
}

func (b *buffered) Read(p []byte) (int, error) { return b.r.Read(p) }

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
	}
	t.Fatal("the server heard nothing")
	var zero T
	return zero
}

func cfg(url string) Config { return Config{Url: url, Timeout: time.Second} }

func TestOpenRefusesABadConfigBeforeDialing(t *testing.T) {
	f := serve(t, true)
	host := f.Addr().String()
	for name, c := range map[string]struct {
		url     string
		timeout time.Duration
		want    string
	}{
		"no url":           {"", time.Second, "Url is required"},
		"http":             {"http://" + host, time.Second, "is not nats://"},
		"no host":          {"nats://", time.Second, "is not nats://"},
		"second bad":       {"nats://" + host + ",http://" + host, time.Second, "is not nats://"},
		"not a url":        {"nats://%zz", time.Second, "not a URL"},
		"credentials":      {"nats://u:secret-in-file@" + host, time.Second, "must not carry credentials"},
		"no timeout":       {"nats://" + host, 0, "Timeout"},
		"bad scheme cred":  {"http://u:secret-in-file@" + host, time.Second, "must not carry credentials"},
		"no scheme cred":   {"scorix:secret-in-file@" + host, time.Second, "must not carry credentials"},
		"second no scheme": {"nats://" + host + ",scorix:secret-in-file@" + host, time.Second, "must not carry credentials"},
		"bad scheme":       {"ftp://secret-in-file:1", time.Second, "server 1 in Url is not nats://"},
	} {
		_, err := Open(context.Background(), Config{Url: c.url, Timeout: c.timeout}, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "secret-in-file") {
			t.Errorf("%s: the error carries the password: %v", name, err)
		}
	}
	select {
	case <-f.first:
		t.Fatal("dialed the server for a config it refused")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUserSendsUserAndPassword(t *testing.T) {
	f := serve(t, true)
	c := cfg(f.url())
	c.User, c.Name = "svc", "shorten-rpc"
	nc, err := Open(context.Background(), c, "right-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	got := recv(t, f.connect)
	for _, want := range []string{`"user":"svc"`, `"pass":"right-pass"`, `"name":"shorten-rpc"`} {
		if !strings.Contains(got, want) {
			t.Errorf("CONNECT %s lacks %s", got, want)
		}
	}
	if strings.Contains(got, "auth_token") {
		t.Errorf("CONNECT %s sends a token as well", got)
	}
}

func TestPassWithoutUserIsAToken(t *testing.T) {
	f := serve(t, true)
	nc, err := Open(context.Background(), cfg(f.url()), "the-token")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	got := recv(t, f.connect)
	if !strings.Contains(got, `"auth_token":"the-token"`) || strings.Contains(got, `"pass"`) {
		t.Fatalf("CONNECT %s, want only auth_token", got)
	}
}

func TestNoPassSendsNoCredentials(t *testing.T) {
	f := serve(t, true)
	nc, err := Open(context.Background(), cfg(f.url()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if got := recv(t, f.connect); strings.Contains(got, "auth_token") || strings.Contains(got, `"pass"`) {
		t.Fatalf("CONNECT %s carries credentials", got)
	}
}

func TestAServerThatNeverAnswersFailsWithinTimeout(t *testing.T) {
	f := serve(t, false)
	c := cfg(f.url())
	c.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := Open(context.Background(), c, "")
	if err == nil {
		t.Fatal("opened a server that never answered")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("took %v with Timeout 300ms", took)
	}
	if !strings.Contains(err.Error(), c.Url) {
		t.Fatalf("err = %v, want it to name %s", err, c.Url)
	}
}

func TestAShorterContextDeadlineWins(t *testing.T) {
	f := serve(t, false)
	c := cfg(f.url())
	c.Timeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Open(ctx, c, ""); err == nil {
		t.Fatal("opened a server that never answered")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("took %v with a 300ms context", took)
	}
}

func TestAnEndedContextDialsNothing(t *testing.T) {
	f := serve(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, cfg(f.url()), ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	select {
	case <-f.first:
		t.Fatal("dialed with an ended context")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTlsUpgradesWithTheHostAsServerName(t *testing.T) {
	f := serve(t, true)
	_, port, _ := net.SplitHostPort(f.Addr().String())
	c := cfg("nats://localhost:" + port)
	c.Tls = true
	if _, err := Open(context.Background(), c, ""); err == nil {
		t.Fatal("opened through a failed handshake")
	}
	if b := recv(t, f.first); b != 0x16 {
		t.Fatalf("first byte %#x, want a TLS handshake", b)
	}
	if sni := recv(t, f.sni); sni != "localhost" {
		t.Fatalf("server name %q, want localhost", sni)
	}
}

func TestATlsSchemeIsTakenAndSpeaksTLS(t *testing.T) {
	f := serve(t, true)
	if _, err := Open(context.Background(), cfg("tls://"+f.Addr().String()), ""); err == nil || strings.Contains(err.Error(), "is not nats://") {
		t.Fatalf("err = %v, want a failed handshake", err)
	}
	if b := recv(t, f.first); b != 0x16 {
		t.Fatalf("first byte %#x, want a TLS handshake", b)
	}
}

func TestWithoutTlsTheClientSpeaksPlainProtocol(t *testing.T) {
	f := serve(t, true)
	nc, err := Open(context.Background(), cfg(f.url()), "")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	if b := recv(t, f.first); b != 'C' {
		t.Fatalf("first byte %q, want CONNECT", b)
	}
}

func server(t *testing.T) Config {
	t.Helper()
	url := os.Getenv("SCORIX_TEST_NATS")
	if url == "" {
		t.Skip("SCORIX_TEST_NATS is not set; connector/brokers.sh starts the server and prints it")
	}
	c := cfg(url)
	c.User = "scorix"
	return c
}

func TestARealServerTakesTheRightPasswordOnly(t *testing.T) {
	c := server(t)
	_, err := Open(context.Background(), c, "wrong-pass")
	if err == nil {
		t.Fatal("opened with a wrong password")
	}
	if strings.Contains(err.Error(), "wrong-pass") {
		t.Fatalf("the error carries the password: %v", err)
	}
	nc, err := Open(context.Background(), c, "right-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	sub, err := nc.SubscribeSync("scorix.test")
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.Publish("scorix.test", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	var m *Msg
	if m, err = sub.NextMsg(3 * time.Second); err != nil || string(m.Data) != "hi" {
		t.Fatalf("got %v, %v", m, err)
	}
}

func TestSeveralSilentServersEndWithinTheShorterOfCtxAndTimeout(t *testing.T) {
	url := strings.Join([]string{serve(t, false).url(), serve(t, false).url(), serve(t, false).url()}, ",")
	for name, c := range map[string]struct {
		timeout time.Duration
		ctx     func() (context.Context, context.CancelFunc)
	}{
		"ctx deadline": {3 * time.Second, func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 400*time.Millisecond)
		}},
		"timeout": {400 * time.Millisecond, func() (context.Context, context.CancelFunc) {
			return context.WithCancel(context.Background())
		}},
		"cancel": {3 * time.Second, func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(400*time.Millisecond, cancel)
			return ctx, cancel
		}},
	} {
		ctx, cancel := c.ctx()
		start := time.Now()
		_, err := Open(ctx, Config{Url: url, Timeout: c.timeout}, "")
		took := time.Since(start)
		cancel()
		if err == nil {
			t.Fatalf("%s: opened servers that never answered", name)
		}
		if took > 800*time.Millisecond {
			t.Errorf("%s: took %v, want about 400ms", name, took)
		}
	}
}

func TestAConnectionThatArrivesAfterCancelIsClosed(t *testing.T) {
	f := serveAfter(t, true, 400*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(150*time.Millisecond, cancel)
	if _, err := Open(ctx, Config{Url: f.url(), Timeout: 3 * time.Second}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	recv(t, f.connect)
	recv(t, f.gone)
}
