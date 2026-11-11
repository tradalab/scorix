package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type silent struct {
	net.Listener
	first chan []byte
	sni   chan string
}

func listen(t *testing.T) *silent {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &silent{Listener: ln, first: make(chan []byte, 64), sni: make(chan string, 64)}
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
	defer conn.Close()
	b := make([]byte, 512)
	n, err := conn.Read(b)
	if err != nil {
		return
	}
	s.first <- b[:n]
	if b[0] == 0x16 {
		tls.Server(&replay{Conn: conn, head: b[:n]}, &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			s.sni <- h.ServerName
			return nil, errors.New("no certificate here")
		}}).Handshake()
		return
	}
	time.Sleep(3 * time.Second)
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

func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(3 * time.Second):
	}
	t.Fatal("the broker heard nothing")
	var zero T
	return zero
}

func cfg(brokers ...string) Config { return Config{Brokers: brokers, Timeout: 300 * time.Millisecond} }

func TestOpenRefusesABadConfigBeforeDialing(t *testing.T) {
	ln := listen(t)
	b := ln.Addr().String()
	for name, c := range map[string]struct {
		cfg  Config
		want string
	}{
		"no timeout":        {Config{Brokers: []string{b}}, "Timeout"},
		"no brokers":        {cfg(), "Brokers is required"},
		"no port":           {cfg("localhost"), "is not host:port"},
		"second bad":        {cfg(b, "localhost"), "is not host:port"},
		"user without mech": {Config{Brokers: []string{b}, Timeout: time.Second, User: "u"}, "go together"},
		"mech without user": {Config{Brokers: []string{b}, Timeout: time.Second, Mechanism: "PLAIN"}, "go together"},
		"unknown mechanism": {Config{Brokers: []string{b}, Timeout: time.Second, User: "u", Mechanism: "plain"}, "is not PLAIN"},
	} {
		_, err := Open(context.Background(), c.cfg, "")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, c.want)
		}
	}
	select {
	case <-ln.first:
		t.Fatal("dialed a broker for a config it refused")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEachMechanismIsTheOneNamed(t *testing.T) {
	for _, name := range []string{"PLAIN", "SCRAM-SHA-256", "SCRAM-SHA-512"} {
		m, err := check(Config{Brokers: []string{"h:1"}, Timeout: time.Second, User: "u", Mechanism: name}, "p")
		if err != nil || m == nil || m.Name() != name {
			t.Errorf("%s: got %v, %v", name, m, err)
		}
	}
	if m, err := check(cfg("h:1"), ""); m != nil || err != nil {
		t.Errorf("no mechanism: got %v, %v", m, err)
	}
}

func TestABrokerThatNeverAnswersFailsWithinTimeout(t *testing.T) {
	ln := listen(t)
	c := cfg(ln.Addr().String())
	start := time.Now()
	_, err := Open(context.Background(), c, "")
	if err == nil {
		t.Fatal("opened a broker that never answered")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("took %v with Timeout 300ms", took)
	}
	if !strings.Contains(err.Error(), c.Brokers[0]) {
		t.Fatalf("err = %v, want it to name %s", err, c.Brokers[0])
	}
}

func TestAShorterContextDeadlineWins(t *testing.T) {
	ln := listen(t)
	c := cfg(ln.Addr().String())
	c.Timeout = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Open(ctx, c, ""); err == nil {
		t.Fatal("opened a broker that never answered")
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("took %v with a 300ms context", took)
	}
}

func TestClientIDRidesInTheRequests(t *testing.T) {
	ln := listen(t)
	c := cfg(ln.Addr().String())
	c.ClientID = "shorten-rpc"
	Open(context.Background(), c, "")
	if b := recv(t, ln.first); !strings.Contains(string(b), "shorten-rpc") {
		t.Fatalf("first request %q lacks the client id", b)
	}
}

func TestWithoutTlsTheClientSpeaksKafka(t *testing.T) {
	ln := listen(t)
	Open(context.Background(), cfg(ln.Addr().String()), "")
	if b := recv(t, ln.first); b[0] != 0x00 {
		t.Fatalf("first byte %#x, want a Kafka length prefix", b[0])
	}
}

func TestTlsSpeaksTLSWithTheHostAsServerName(t *testing.T) {
	ln := listen(t)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	c := cfg(net.JoinHostPort("localhost", port))
	c.Tls = true
	if _, err := Open(context.Background(), c, ""); err == nil {
		t.Fatal("opened through a failed handshake")
	}
	if b := recv(t, ln.first); b[0] != 0x16 {
		t.Fatalf("first byte %#x, want a TLS handshake", b[0])
	}
	if sni := recv(t, ln.sni); sni != "localhost" {
		t.Fatalf("server name %q, want localhost", sni)
	}
}

func broker(t *testing.T, env string) Config {
	t.Helper()
	addr := os.Getenv(env)
	if addr == "" {
		t.Skip(env + " is not set; connector/brokers.sh starts the broker and prints it")
	}
	c := cfg(addr)
	c.Timeout = 20 * time.Second
	return c
}

func TestOptsReachTheClient(t *testing.T) {
	c := broker(t, "SCORIX_TEST_KAFKA")
	ctx := context.Background()
	topic := "scorix-test-" + time.Now().Format("150405.000000")
	cl, err := Open(ctx, c, "", kgo.AllowAutoTopicCreation(), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := cl.ProduceSync(ctx, &Record{Topic: topic, Value: []byte("hi")}).FirstErr(); err != nil {
		t.Fatal(err)
	}
	poll, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		f := cl.PollFetches(poll)
		if err := poll.Err(); err != nil {
			t.Fatal("no record came back")
		}
		if rs := f.Records(); len(rs) > 0 {
			if string(rs[0].Value) != "hi" {
				t.Fatalf("got %q", rs[0].Value)
			}
			return
		}
	}
}

func TestPlainTakesTheRightPasswordOnly(t *testing.T) {
	c := broker(t, "SCORIX_TEST_KAFKA_SASL")
	c.User, c.Mechanism = "scorix", "PLAIN"
	c.Timeout = 5 * time.Second
	_, err := Open(context.Background(), c, "wrong-pass")
	if err == nil {
		t.Fatal("opened with a wrong password")
	}
	if strings.Contains(err.Error(), "wrong-pass") {
		t.Fatalf("the error carries the password: %v", err)
	}
	cl, err := Open(context.Background(), c, "right-pass")
	if err != nil {
		t.Fatal(err)
	}
	cl.Close()
}

func TestScramTakesTheRightPasswordOnly(t *testing.T) {
	for _, mechanism := range []string{"SCRAM-SHA-256", "SCRAM-SHA-512"} {
		c := broker(t, "SCORIX_TEST_KAFKA_SASL")
		c.User, c.Mechanism = "scorix", mechanism
		c.Timeout = 5 * time.Second
		if _, err := Open(context.Background(), c, "wrong-pass"); err == nil || strings.Contains(err.Error(), "wrong-pass") {
			t.Fatalf("%s with a wrong password = %v", mechanism, err)
		}
		cl, err := Open(context.Background(), c, "right-pass")
		if err != nil {
			t.Fatalf("%s: %v", mechanism, err)
		}
		cl.Close()
	}
}

func TestASaslListenerRefusesAClientWithoutSasl(t *testing.T) {
	c := broker(t, "SCORIX_TEST_KAFKA_SASL")
	c.Timeout = 3 * time.Second
	if _, err := Open(context.Background(), c, ""); err == nil {
		t.Fatal("a SASL listener took a client that sent no credentials")
	}
}
