package netx

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

func tunnelled(t *testing.T, target string) (*bastion, net.Conn) {
	t.Helper()
	b := startBastion(t)
	c, err := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()}).Dial(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return b, c
}

func TestAnExpiredDeadlineOnAnIdleConnectionDoesNotKillIt(t *testing.T) {
	_, c := tunnelled(t, echoServer(t))
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	roundTrip(t, c, "after a timeout the connection still works")
}

func TestADeadlineLeftSetAfterACommandDoesNotKillTheIdleConnection(t *testing.T) {
	_, c := tunnelled(t, echoServer(t))
	_ = c.SetDeadline(time.Now().Add(80 * time.Millisecond))
	roundTrip(t, c, "command")
	time.Sleep(200 * time.Millisecond)
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	roundTrip(t, c, "next command on the pooled connection")
}

func TestAClearedDeadlineNeverFiresLate(t *testing.T) {
	_, c := tunnelled(t, echoServer(t))
	for range 50 {
		_ = c.SetDeadline(time.Now().Add(time.Millisecond))
		_ = c.SetDeadline(time.Time{})
	}
	time.Sleep(50 * time.Millisecond)
	roundTrip(t, c, "no stale timer")
}

func TestExtendingTheDeadlineWhileReadWaitsTakesEffect(t *testing.T) {
	_, c := tunnelled(t, echoServer(t))
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
	got := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := c.Read(buf)
		got <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	time.Sleep(100 * time.Millisecond)
	if _, err := c.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("read failed with the old deadline: %v", err)
	}
}

func TestAWriteDeadlineThatPassesWhileIdleDoesNotKillTheConnection(t *testing.T) {
	_, c := tunnelled(t, echoServer(t))
	_ = c.SetWriteDeadline(time.Now().Add(30 * time.Millisecond))
	time.Sleep(80 * time.Millisecond)
	if _, err := c.Write([]byte("x")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write past its deadline: %v", err)
	}
	_ = c.SetWriteDeadline(time.Time{})
	roundTrip(t, c, "writes again once the deadline is cleared")
}

func TestRemoteAddrNamesTheTargetNotTheTunnel(t *testing.T) {
	echo := echoServer(t)
	_, c := tunnelled(t, echo)
	if c.RemoteAddr().String() != echo || c.RemoteAddr().Network() != "tcp" {
		t.Fatalf("RemoteAddr = %v", c.RemoteAddr())
	}
}

func TestTheFirstDialAfterADropRecovers(t *testing.T) {
	b := startBastion(t)
	echo := echoServer(t)
	r := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()})
	c, err := r.Dial(context.Background(), "tcp", echo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	for range 5 {
		b.drop()
		c, err := r.Dial(context.Background(), "tcp", echo)
		if err != nil {
			t.Fatalf("first dial right after the drop: %v", err)
		}
		roundTrip(t, c, "recovered")
		_ = c.Close()
	}
}

func TestConcurrentDialsOnANewRouteOpenOneSession(t *testing.T) {
	b := startBastion(t)
	echo := echoServer(t)
	r := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()})
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := r.Dial(context.Background(), "tcp", echo)
			if err == nil {
				_ = c.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := b.logins.Load(); got != 1 {
		t.Fatalf("%d logins for 10 concurrent dials, want 1", got)
	}
}

func TestCancellingTheContextStopsAHangingHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	r, err := Open(Config{Timeout: 30 * time.Second, SSH: &SSH{Addr: ln.Addr().String(), Username: "u", Password: "p", HostKey: HostKeyPolicy{Insecure: true}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r.Dial(ctx, "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("a silent bastion was accepted")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("cancel took %s with a 30s timeout", took)
	}
}

func TestCloseReleasesTheSharedHTTPPool(t *testing.T) {
	r, err := Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	a, b := r.HTTPClient(time.Second), r.HTTPClient(time.Second)
	if a.Transport != b.Transport {
		t.Fatal("each HTTPClient got its own pool")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestATLSProxyIsRefusedByName(t *testing.T) {
	if _, err := Open(Config{Proxy: &Proxy{Protocol: "https", Addr: "p:443"}}); err == nil {
		t.Fatal("https proxy accepted as plain CONNECT")
	}
}

func stuckServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held []net.Conn
	var mu sync.Mutex
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, c)
			mu.Unlock()
		}
	}()
	return ln.Addr().String()
}

func TestExtendingTheWriteDeadlineWhileAWriteBlocksTakesEffect(t *testing.T) {
	_, c := tunnelled(t, stuckServer(t))
	_ = c.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
	start := time.Now()
	got := make(chan error, 1)
	go func() {
		_, err := c.Write(make([]byte, 64<<20))
		got <- err
	}()
	time.Sleep(30 * time.Millisecond)
	_ = c.SetWriteDeadline(time.Now().Add(800 * time.Millisecond))
	err := <-got
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("blocked write ended with %v", err)
	}
	if took := time.Since(start); took < 500*time.Millisecond {
		t.Fatalf("write gave up after %s, on the deadline that was replaced", took)
	}
}

func TestAWaiterDoesNotInheritTheCancelOfTheDialThatWasOpeningTheSession(t *testing.T) {
	b := startBastion(t)
	echo := echoServer(t)
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = front.Close() })
	go func() {
		for n := 0; ; n++ {
			c, err := front.Accept()
			if err != nil {
				return
			}
			if n == 0 {
				t.Cleanup(func() { _ = c.Close() })
				continue
			}
			go func() {
				up, err := net.Dial("tcp", b.addr)
				if err != nil {
					_ = c.Close()
					return
				}
				go func() { _, _ = io.Copy(up, c); _ = up.Close() }()
				_, _ = io.Copy(c, up)
				_ = c.Close()
			}()
		}
	}()
	r, err := Open(Config{Timeout: 3 * time.Second, SSH: &SSH{Addr: front.Addr().String(), Username: "u", Password: "p", HostKey: HostKeyPolicy{Pinned: b.pin()}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := r.Dial(ctx, "tcp", echo)
		first <- err
	}()
	time.Sleep(50 * time.Millisecond)
	c, err := r.Dial(context.Background(), "tcp", echo)
	if err != nil {
		t.Fatalf("the waiting dial failed with the opener's cancel: %v", err)
	}
	roundTrip(t, c, "own context")
	_ = c.Close()
	if err := <-first; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the opener ended with %v", err)
	}
}
