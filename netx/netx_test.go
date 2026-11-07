package netx

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type bastion struct {
	addr    string
	hostKey ssh.PublicKey
	logins  atomic.Int32
	mu      sync.Mutex
	conns   []*ssh.ServerConn
}

func startBastion(t *testing.T) *bastion {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	b := &bastion{hostKey: signer.PublicKey()}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			b.logins.Add(1)
			if c.User() == "u" && string(pw) == "p" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			b.logins.Add(1)
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	b.addr = ln.Addr().String()
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(nc, cfg)
		}
	}()
	return b
}

func (b *bastion) pin() string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(b.hostKey))) }

func (b *bastion) drop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		_ = c.Close()
	}
	b.conns = nil
}

func (b *bastion) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		_ = nc.Close()
		return
	}
	b.mu.Lock()
	b.conns = append(b.conns, sc)
	b.mu.Unlock()
	go func() {
		for r := range reqs {
			if r.WantReply {
				_ = r.Reply(r.Type == "keepalive@openssh.com", nil)
			}
		}
	}()
	for nch := range chans {
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if nch.ChannelType() != "direct-tcpip" || ssh.Unmarshal(nch.ExtraData(), &p) != nil {
			_ = nch.Reject(ssh.UnknownChannelType, "direct-tcpip only")
			continue
		}
		target, err := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
		if err != nil {
			_ = nch.Reject(ssh.ConnectionFailed, err.Error())
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			_ = target.Close()
			continue
		}
		go ssh.DiscardRequests(creqs)
		go func() {
			defer ch.Close()
			defer target.Close()
			go func() { _, _ = io.Copy(ch, target); _ = ch.CloseWrite() }()
			_, _ = io.Copy(target, ch)
		}()
	}
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func silentServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c) }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
}

func sshRoute(t *testing.T, b *bastion, hk HostKeyPolicy) *Route {
	t.Helper()
	r, err := Open(Config{Timeout: 3 * time.Second, SSH: &SSH{Addr: b.addr, Username: "u", Password: "p", HostKey: hk}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestAnUnpinnedBastionIsRefusedWithTheKeyToPin(t *testing.T) {
	b := startBastion(t)
	_, err := sshRoute(t, b, HostKeyPolicy{}).Dial(context.Background(), "tcp", echoServer(t))
	var hk *HostKeyError
	if !errors.As(err, &hk) || hk.Changed {
		t.Fatalf("err = %v, want an untrusted-key HostKeyError", err)
	}
	if hk.AuthorizedKey() != b.pin() || !strings.HasPrefix(hk.Fingerprint(), "SHA256:") {
		t.Fatalf("key to pin = %q (%s), want the bastion's", hk.AuthorizedKey(), hk.Fingerprint())
	}
}

func TestThePinnedKeyTunnelsAndADifferentPinSaysChanged(t *testing.T) {
	b := startBastion(t)
	echo := echoServer(t)
	c, err := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()}).Dial(context.Background(), "tcp", echo)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, "through the bastion")

	other := startBastion(t)
	_, err = sshRoute(t, b, HostKeyPolicy{Pinned: other.pin()}).Dial(context.Background(), "tcp", echo)
	var hk *HostKeyError
	if !errors.As(err, &hk) || !hk.Changed {
		t.Fatalf("err = %v, want a changed-key HostKeyError", err)
	}
}

func TestSkippingTheHostKeyCheckIsAnExplicitChoice(t *testing.T) {
	b := startBastion(t)
	c, err := sshRoute(t, b, HostKeyPolicy{Insecure: true}).Dial(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestAnEncryptedKeyWithoutItsPassphraseSaysSo(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(key, "", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(Config{SSH: &SSH{Addr: "127.0.0.1:22", Username: "u", PrivateKey: string(pem.EncodeToMemory(block)), HostKey: HostKeyPolicy{Insecure: true}}})
	if err == nil || !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("err = %v", err)
	}
	if _, err := Open(Config{SSH: &SSH{Addr: "127.0.0.1:22", Username: "u", HostKey: HostKeyPolicy{Insecure: true}}}); err == nil {
		t.Fatal("ssh with no credential accepted")
	}
}

func TestOneSessionIsSharedAndADroppedSessionIsReopened(t *testing.T) {
	b := startBastion(t)
	echo := echoServer(t)
	r := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()})
	for i := range 3 {
		c, err := r.Dial(context.Background(), "tcp", echo)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		_ = c.Close()
	}
	if got := b.logins.Load(); got != 1 {
		t.Fatalf("%d logins for 3 dials, want one shared session", got)
	}

	b.drop()
	time.Sleep(100 * time.Millisecond)
	c, err := r.Dial(context.Background(), "tcp", echo)
	if err != nil {
		t.Fatalf("dial after the bastion dropped the session: %v", err)
	}
	defer c.Close()
	roundTrip(t, c, "after reconnect")
	if got := b.logins.Load(); got != 2 {
		t.Fatalf("%d logins, want a second one after the drop", got)
	}
}

func TestADeadlineThroughTheTunnelTimesOutInsteadOfHanging(t *testing.T) {
	b := startBastion(t)
	c, err := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()}).Dial(context.Background(), "tcp", silentServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	start := time.Now()
	_, err = c.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatal("a deadline error that is not a net.Error timeout makes client libraries retry the wrong way")
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("read returned after %s", took)
	}
}

func TestClearingADeadlineKeepsTheConnectionUsable(t *testing.T) {
	b := startBastion(t)
	c, err := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()}).Dial(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(50 * time.Millisecond))
	_ = c.SetDeadline(time.Time{})
	time.Sleep(100 * time.Millisecond)
	roundTrip(t, c, "still open")
}

func httpConnectProxy(t *testing.T, extra string, status int) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				if status != http.StatusOK {
					fmt.Fprintf(c, "HTTP/1.1 %d %s\r\n\r\n", status, http.StatusText(status))
					return
				}
				target, err := net.Dial("tcp", req.Host)
				if err != nil {
					return
				}
				defer target.Close()
				fmt.Fprintf(c, "HTTP/1.1 200 Connection established\r\n\r\n%s", extra)
				go func() { _, _ = io.Copy(target, c) }()
				_, _ = io.Copy(c, target)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestHTTPConnectKeepsBytesThatArriveWithTheReply(t *testing.T) {
	r, err := Open(Config{Proxy: &Proxy{Protocol: "http", Addr: httpConnectProxy(t, "HELLO", http.StatusOK)}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Dial(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	got := make([]byte, 5)
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "HELLO" {
		t.Fatalf("first bytes = %q, %v; they were read with the proxy reply and lost", got, err)
	}
	roundTrip(t, c, "and then the echo")
}

func TestARefusedConnectNamesTheProxyAndStatus(t *testing.T) {
	r, _ := Open(Config{Proxy: &Proxy{Protocol: "http", Addr: httpConnectProxy(t, "", http.StatusProxyAuthRequired)}})
	_, err := r.Dial(context.Background(), "tcp", "10.0.0.1:9092")
	if err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("err = %v", err)
	}
}

func socks5Proxy(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				hdr := make([]byte, 2)
				if _, err := io.ReadFull(c, hdr); err != nil {
					return
				}
				methods := make([]byte, hdr[1])
				_, _ = io.ReadFull(c, methods)
				_, _ = c.Write([]byte{5, 0})
				req := make([]byte, 4)
				if _, err := io.ReadFull(c, req); err != nil {
					return
				}
				var host string
				switch req[3] {
				case 1:
					ip := make([]byte, 4)
					_, _ = io.ReadFull(c, ip)
					host = net.IP(ip).String()
				case 3:
					n := make([]byte, 1)
					_, _ = io.ReadFull(c, n)
					name := make([]byte, n[0])
					_, _ = io.ReadFull(c, name)
					host = string(name)
				default:
					return
				}
				pb := make([]byte, 2)
				_, _ = io.ReadFull(c, pb)
				target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb)))))
				if err != nil {
					_, _ = c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer target.Close()
				_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go func() { _, _ = io.Copy(target, c) }()
				_, _ = io.Copy(c, target)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestTheBastionIsReachedThroughASocks5Proxy(t *testing.T) {
	b := startBastion(t)
	r, err := Open(Config{
		Proxy: &Proxy{Protocol: "socks5", Addr: socks5Proxy(t)},
		SSH:   &SSH{Addr: b.addr, Username: "u", Password: "p", HostKey: HostKeyPolicy{Pinned: b.pin()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c, err := r.Dial(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, "proxy then bastion")
}

func tlsServer(t *testing.T, name string) (addr, caPEM string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String(), string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestAnAddressRewriteKeepsTheOriginalNameForTLS(t *testing.T) {
	real, ca := tlsServer(t, "broker-1.internal")
	r, err := Open(Config{
		AddrMap: map[string]string{"broker-1.internal:9093": real},
		TLS:     &TLS{CACert: ca},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Dial(context.Background(), "tcp", "broker-1.internal:9093")
	if err != nil {
		t.Fatalf("dial through the rewrite: %v", err)
	}
	defer c.Close()
	roundTrip(t, c, "tls after rewrite")

	if _, err := r.Dial(context.Background(), "tcp", real); err == nil {
		t.Fatal("verification passed for an address the certificate does not name")
	}
}

func TestMapAddrRunsAfterTheTable(t *testing.T) {
	echo := echoServer(t)
	var saw string
	r, _ := Open(Config{
		AddrMap: map[string]string{"a:1": "b:2"},
		MapAddr: func(addr string) string { saw = addr; return echo },
	})
	c, err := r.Dial(context.Background(), "tcp", "a:1")
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	if saw != "b:2" {
		t.Fatalf("MapAddr saw %q, want the table's output", saw)
	}
}

func TestHTTPClientGoesThroughTheTunnelWithoutTheTargetTLS(t *testing.T) {
	b := startBastion(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "token") }))
	defer srv.Close()
	r, err := Open(Config{
		SSH: &SSH{Addr: b.addr, Username: "u", Password: "p", HostKey: HostKeyPolicy{Pinned: b.pin()}},
		TLS: &TLS{ClientCert: "", ServerName: "broker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	res, err := r.HTTPClient(5 * time.Second).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if string(body) != "token" || b.logins.Load() != 1 {
		t.Fatalf("body %q, logins %d", body, b.logins.Load())
	}
}

func TestCloseIsIdempotentAndADialAfterItFails(t *testing.T) {
	b := startBastion(t)
	r := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()})
	if c, err := r.Dial(context.Background(), "tcp", echoServer(t)); err == nil {
		_ = c.Close()
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := r.Dial(context.Background(), "tcp", "127.0.0.1:1"); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestConfigErrorsAreCaughtAtOpen(t *testing.T) {
	for name, cfg := range map[string]Config{
		"proxy protocol": {Proxy: &Proxy{Protocol: "ftp", Addr: "p:1"}},
		"proxy address":  {Proxy: &Proxy{Protocol: "socks5", Addr: "nope"}},
		"bad pin":        {SSH: &SSH{Addr: "h:22", Username: "u", Password: "p", HostKey: HostKeyPolicy{Pinned: "garbage"}}},
		"half a cert":    {TLS: &TLS{ClientCert: "x"}},
		"bad ca":         {TLS: &TLS{CACert: "not pem"}},
	} {
		if _, err := Open(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParseAddrMap(t *testing.T) {
	got, err := ParseAddrMap("# brokers\nb-1:9092 = 127.0.0.1:19092\n\n[::1]:9092=127.0.0.1:2\n")
	if err != nil || got["b-1:9092"] != "127.0.0.1:19092" || got["[::1]:9092"] != "127.0.0.1:2" {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, bad := range []string{"novalue", "a=", "a:1=b", "host=1.2.3.4:5"} {
		if _, err := ParseAddrMap(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
