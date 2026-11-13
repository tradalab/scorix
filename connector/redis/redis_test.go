package redis

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func valid(host string) Config { return Config{Host: host, Timeout: time.Second} }

func TestOpenRefusesABadConfigBeforeDialing(t *testing.T) {
	for name, c := range map[string]struct {
		mutate func(*Config)
		want   string
	}{
		"no host":       {func(c *Config) { c.Host = "" }, "not host:port"},
		"no port":       {func(c *Config) { c.Host = "localhost" }, "not host:port"},
		"negative db":   {func(c *Config) { c.DB = -1 }, "DB"},
		"negative pool": {func(c *Config) { c.PoolSize = -1 }, "PoolSize"},
		"no timeout":    {func(c *Config) { c.Timeout = 0 }, "Timeout"},
	} {
		ln := listen(t)
		cfg := valid(ln.Addr().String())
		c.mutate(&cfg)
		_, err := Open(context.Background(), cfg, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
		if ln.accepted() {
			t.Errorf("%s: dialed the server for a config it refused", name)
		}
	}
}

type silent struct {
	net.Listener
	first chan byte
	sni   chan string
}

func listen(t *testing.T) *silent {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &silent{Listener: ln, first: make(chan byte, 16), sni: make(chan string, 16)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.hold(conn)
		}
	}()
	return s
}

func (s *silent) hold(conn net.Conn) {
	b := make([]byte, 1)
	if _, err := conn.Read(b); err != nil {
		return
	}
	s.first <- b[0]
	if b[0] == 0x16 {
		tls.Server(&replay{Conn: conn, head: b}, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			s.sni <- h.ServerName
			return nil, errors.New("no certificate here")
		}}).Handshake()
		conn.Close()
		return
	}
	time.Sleep(5 * time.Second)
	conn.Close()
}

func (s *silent) accepted() bool {
	select {
	case <-s.first:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

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

type replay struct {
	net.Conn
	head []byte
}

func (r *replay) Read(p []byte) (int, error) {
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]
		return n, nil
	}
	return r.Conn.Read(p)
}

func open(t *testing.T, cfg Config) *Client {
	t.Helper()
	c, err := Open(context.Background(), cfg, "")
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestOpenDialsNothingSoAServerThatIsDownDoesNotFailIt(t *testing.T) {
	ln := listen(t)
	open(t, valid(ln.Addr().String()))
	if ln.accepted() {
		t.Fatal("Open dialed the server")
	}
	open(t, valid(refused(t)))
}

func TestAServerThatNeverAnswersFailsPingWithinTimeout(t *testing.T) {
	ln := listen(t)
	cfg := valid(ln.Addr().String())
	cfg.Timeout = 200 * time.Millisecond
	start := time.Now()
	err := Ping(context.Background(), open(t, cfg))
	if err == nil {
		t.Fatal("opened a server that never answered")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("took %v with Timeout 200ms", took)
	}
	if !strings.Contains(err.Error(), cfg.Host) {
		t.Fatalf("err = %v, want it to name %s", err, cfg.Host)
	}
}

func TestTlsSpeaksTLSWithTheHostAsServerName(t *testing.T) {
	ln := listen(t)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := valid(net.JoinHostPort("localhost", port))
	cfg.Tls = true
	cfg.Timeout = 200 * time.Millisecond
	if err := Ping(context.Background(), open(t, cfg)); err == nil {
		t.Fatal("pinged through a failed handshake")
	}
	if b := recv(t, ln.first); b != 0x16 {
		t.Fatalf("first byte %#x, want a TLS handshake", b)
	}
	if sni := recv(t, ln.sni); sni != "localhost" {
		t.Fatalf("server name %q, want localhost", sni)
	}
}

func TestWithoutTlsTheFirstByteIsRESP(t *testing.T) {
	ln := listen(t)
	cfg := valid(ln.Addr().String())
	cfg.Timeout = 200 * time.Millisecond
	Ping(context.Background(), open(t, cfg))
	if b := recv(t, ln.first); b != '*' {
		t.Fatalf("first byte %#x, want a RESP array", b)
	}
}

func server(t *testing.T) Config {
	t.Helper()
	addr := os.Getenv("SCORIX_TEST_REDIS")
	if addr == "" {
		t.Skip("SCORIX_TEST_REDIS is not set; connector/brokers.sh starts the server and prints it")
	}
	return valid(addr)
}

func TestUserAndPasswordReachTheServer(t *testing.T) {
	cfg := server(t)
	ctx := context.Background()
	admin, err := Open(ctx, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := admin.Do(ctx, "ACL", "SETUSER", "scorixtest", "reset", "on", ">right-pass", "~*", "+@all").Err(); err != nil {
		t.Fatal(err)
	}
	defer admin.Do(ctx, "ACL", "DELUSER", "scorixtest")
	cfg.User = "scorixtest"
	c, err := Open(ctx, cfg, "right-pass")
	if err != nil {
		t.Fatalf("right password: %v", err)
	}
	defer c.Close()
	who, err := c.Do(ctx, "ACL", "WHOAMI").Text()
	if err != nil || who != "scorixtest" {
		t.Fatalf("whoami = %q, %v", who, err)
	}
	wrong, err := Open(ctx, cfg, "wrong-pass")
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	err = Ping(ctx, wrong)
	if err == nil {
		t.Fatal("opened with a wrong password")
	}
	if strings.Contains(err.Error(), "wrong-pass") {
		t.Fatalf("the error carries the password: %v", err)
	}
}

func TestDBSelectsTheDatabase(t *testing.T) {
	cfg := server(t)
	ctx := context.Background()
	cfg.DB = 1
	one, err := Open(ctx, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	key := "scorix:test:" + t.Name()
	if err := one.Set(ctx, key, "v", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	defer one.Del(ctx, key)
	cfg.DB = 0
	zero, err := Open(ctx, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer zero.Close()
	if err := zero.Get(ctx, key).Err(); !errors.Is(err, Nil) {
		t.Fatalf("db 0 read %v, want Nil", err)
	}
	if v, _ := one.Get(ctx, key).Result(); v != "v" {
		t.Fatalf("db 1 read %q", v)
	}
}

func TestScriptRunsOnTheServer(t *testing.T) {
	cfg := server(t)
	ctx := context.Background()
	c, err := Open(ctx, cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := "scorix:test:" + t.Name()
	defer c.Del(ctx, key)
	incr := NewScript(`return redis.call("INCRBY", KEYS[1], ARGV[1])`)
	n, err := incr.Run(ctx, c, []string{key}, 5).Int64()
	if err != nil || n != 5 {
		t.Fatalf("script = %d, %v", n, err)
	}
}

func refused(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestARefusedPortFailsPingWithinTimeoutDespiteRetries(t *testing.T) {
	cfg := valid(refused(t))
	cfg.Timeout = 200 * time.Millisecond
	c := open(t, cfg)
	start := time.Now()
	if err := Ping(context.Background(), c); err == nil {
		t.Fatal("pinged a closed port")
	}
	if took := time.Since(start); took > 600*time.Millisecond {
		t.Fatalf("took %v with Timeout 200ms", took)
	}
}

func TestAShorterCtxEndsPingBeforeTimeout(t *testing.T) {
	ln := listen(t)
	cfg := valid(ln.Addr().String())
	cfg.Timeout = 2 * time.Second
	c := open(t, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := Ping(ctx, c); err == nil {
		t.Fatal("pinged a server that never answered")
	}
	if took := time.Since(start); took > 800*time.Millisecond {
		t.Fatalf("took %v with a 300ms ctx", took)
	}
}

func TestAFailedPingThenCloseLeavesNoGoroutineBehind(t *testing.T) {
	cfg := valid(refused(t))
	cfg.Timeout = 50 * time.Millisecond
	pingAndClose := func() {
		c, _ := Open(context.Background(), cfg, "")
		Ping(context.Background(), c)
		c.Close()
	}
	pingAndClose()
	time.Sleep(100 * time.Millisecond)
	before := runtime.NumGoroutine()
	for range 5 {
		pingAndClose()
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("%d goroutines after 5 failed Pings, %d before", n, before)
	}
}

func TestSortedSetTypesNeedNoGoRedisImport(t *testing.T) {
	cfg := server(t)
	ctx := context.Background()
	c := open(t, cfg)
	if err := Ping(ctx, c); err != nil {
		t.Fatal(err)
	}
	key := "scorix:test:" + t.Name()
	defer c.Del(ctx, key)
	if err := c.ZAdd(ctx, key, Z{Score: 1, Member: "a"}, Z{Score: 2, Member: "b"}).Err(); err != nil {
		t.Fatal(err)
	}
	got, err := c.ZRangeByScore(ctx, key, &ZRangeBy{Min: "2", Max: "+inf"}).Result()
	if err != nil || len(got) != 1 || got[0] != "b" {
		t.Fatalf("ZRangeByScore = %v, %v", got, err)
	}
}
