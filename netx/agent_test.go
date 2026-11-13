package netx

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type stuckAgent struct {
	agent.Agent
	unblock chan struct{}
}

func (a stuckAgent) Sign(key ssh.PublicKey, data []byte) (*ssh.Signature, error) {
	<-a.unblock
	return a.Agent.Sign(key, data)
}

func startAgent(t *testing.T) (socket, fingerprint string) {
	t.Helper()
	return serveAgent(t, func(ring agent.Agent) agent.Agent { return ring })
}

func serveAgent(t *testing.T, wrap func(agent.Agent) agent.Agent) (socket, fingerprint string) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ring := agent.NewKeyring()
	if err := ring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	served := wrap(ring)
	socket = filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_ = agent.ServeAgent(served, c)
				_ = c.Close()
			}()
		}
	}()
	return socket, ssh.FingerprintSHA256(pub)
}

func TestTheAgentsKeyLogsInWhenNoPasswordOrKeyIsGiven(t *testing.T) {
	b := startBastion(t)
	socket, fingerprint := startAgent(t)
	r, err := Open(Config{Timeout: 3 * time.Second, SSH: &SSH{Addr: b.addr, Username: "u", Agent: socket, HostKey: HostKeyPolicy{Pinned: b.pin()}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	c, err := r.Dial(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, "signed by the agent")
	b.mu.Lock()
	offered := slices.Clone(b.offered)
	b.mu.Unlock()
	if !slices.Contains(offered, fingerprint) {
		t.Fatalf("bastion saw keys %v, want the agent's %s", offered, fingerprint)
	}
}

func TestAnAgentThatIsNotRunningIsSkippedAndNamedWhenNothingElseLogsIn(t *testing.T) {
	b := startBastion(t)
	missing := filepath.Join(t.TempDir(), "gone.sock")
	withPassword, err := Open(Config{Timeout: 3 * time.Second, SSH: &SSH{Addr: b.addr, Username: "u", Password: "p", Agent: missing, HostKey: HostKeyPolicy{Pinned: b.pin()}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = withPassword.Close() })
	if err := withPassword.Check(context.Background()); err != nil {
		t.Fatalf("a dead agent blocked the password: %v", err)
	}

	agentOnly, err := Open(Config{Timeout: 3 * time.Second, SSH: &SSH{Addr: b.addr, Username: "u", Agent: missing, HostKey: HostKeyPolicy{Pinned: b.pin()}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agentOnly.Close() })
	err = agentOnly.Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v, want a failed login naming %s", err, missing)
	}
}

func TestAnAgentStuckOnSigningGivesUpWithTheContextAndFreesTheRoute(t *testing.T) {
	b := startBastion(t)
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	socket, _ := serveAgent(t, func(ring agent.Agent) agent.Agent { return stuckAgent{Agent: ring, unblock: unblock} })
	r, err := Open(Config{Timeout: 30 * time.Second, SSH: &SSH{Addr: b.addr, Username: "u", Agent: socket, HostKey: HostKeyPolicy{Pinned: b.pin()}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	for i := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		start := time.Now()
		err := r.Check(ctx)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d: err = %v, want the context's deadline", i, err)
		}
		if waited := time.Since(start); waited > 3*time.Second {
			t.Fatalf("attempt %d: waited %s on a stuck agent", i, waited)
		}
	}
}

func TestCheckLogsInWithoutDialingAndSurfacesAnUnpinnedKey(t *testing.T) {
	b := startBastion(t)
	if err := sshRoute(t, b, HostKeyPolicy{Pinned: b.pin()}).Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b.logins.Load() == 0 {
		t.Fatal("Check did not log in")
	}
	var hk *HostKeyError
	if err := sshRoute(t, b, HostKeyPolicy{}).Check(context.Background()); !errors.As(err, &hk) {
		t.Fatalf("err = %v, want a HostKeyError", err)
	}
	plain, err := Open(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.Check(context.Background()); err != nil {
		t.Fatalf("Check without ssh = %v", err)
	}
	_ = plain.Close()
	if err := plain.Check(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Check after Close = %v, want ErrClosed", err)
	}
}

func TestTheAgentsKeysAreStillOfferedWhenThePrivateKeyIsRefused(t *testing.T) {
	b := startBastion(t)
	socket, fingerprint := startAgent(t)
	b.mu.Lock()
	b.allow = fingerprint
	b.mu.Unlock()
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(other, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := Open(Config{Timeout: 3 * time.Second, SSH: &SSH{
		Addr: b.addr, Username: "u", PrivateKey: string(pem.EncodeToMemory(block)), Agent: socket, HostKey: HostKeyPolicy{Pinned: b.pin()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.Check(context.Background()); err != nil {
		t.Fatalf("the agent's key was never tried after the private key was refused: %v", err)
	}
}
