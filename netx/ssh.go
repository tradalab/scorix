package netx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// HostKeyPolicy decides whether a bastion is the one the user meant. Set Pinned to the authorized_keys line
// stored for this bastion, or set Insecure to skip the check knowingly. With neither, every key is refused.
type HostKeyPolicy struct {
	Pinned   string
	Insecure bool
}

// HostKeyError is returned when the bastion's key is not pinned yet (Changed false) or differs from the pin
// (Changed true). AuthorizedKey is the line to store once the user has confirmed Fingerprint.
type HostKeyError struct {
	Presented ssh.PublicKey
	Changed   bool
}

func (e *HostKeyError) Error() string {
	if e.Changed {
		return "netx: the ssh host key CHANGED, now " + e.Fingerprint()
	}
	return "netx: the ssh host key is not trusted yet: " + e.Fingerprint()
}

// Fingerprint is the SHA256 form ssh-keygen -lf prints, for showing to the user.
func (e *HostKeyError) Fingerprint() string { return ssh.FingerprintSHA256(e.Presented) }

// AuthorizedKey is the presented key as one authorized_keys line, the value HostKeyPolicy.Pinned takes.
func (e *HostKeyError) AuthorizedKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(e.Presented)))
}

func hostKeyCallback(p HostKeyPolicy) (ssh.HostKeyCallback, []string, error) {
	if p.Insecure {
		return ssh.InsecureIgnoreHostKey(), nil, nil
	}
	var pinned ssh.PublicKey
	if strings.TrimSpace(p.Pinned) != "" {
		key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(p.Pinned))
		if err != nil {
			return nil, nil, fmt.Errorf("netx: pinned host key: %w", err)
		}
		pinned = key
	}
	check := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if pinned == nil {
			return &HostKeyError{Presented: key}
		}
		if !bytes.Equal(pinned.Marshal(), key.Marshal()) {
			return &HostKeyError{Presented: key, Changed: true}
		}
		return nil
	}
	return check, hostKeyAlgorithms(pinned), nil
}

func hostKeyAlgorithms(pinned ssh.PublicKey) []string {
	switch {
	case pinned == nil:
		return nil
	case pinned.Type() == ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	default:
		return []string{pinned.Type()}
	}
}

func buildSSHClientConfig(cfg SSH, timeout time.Duration) (*ssh.ClientConfig, error) {
	callback, algos, err := hostKeyCallback(cfg.HostKey)
	if err != nil {
		return nil, err
	}
	out := &ssh.ClientConfig{User: cfg.Username, Timeout: timeout, HostKeyCallback: callback, HostKeyAlgorithms: algos}
	if key := strings.TrimSpace(cfg.PrivateKey); key != "" {
		var signer ssh.Signer
		if cfg.Passphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(key), []byte(cfg.Passphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(key))
		}
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, errors.New("netx: ssh private key is encrypted and no passphrase was given")
		}
		if err != nil {
			return nil, fmt.Errorf("netx: ssh private key: %w", err)
		}
		out.Auth = append(out.Auth, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		out.Auth = append(out.Auth, ssh.Password(cfg.Password), ssh.KeyboardInteractive(answerWith(cfg.Password)))
	}
	if len(out.Auth) == 0 {
		return nil, errors.New("netx: ssh needs a password or a private key")
	}
	return out, nil
}

func answerWith(password string) ssh.KeyboardInteractiveChallenge {
	return func(_, _ string, questions []string, _ []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range answers {
			answers[i] = password
		}
		return answers, nil
	}
}

type tunnel struct {
	base      contextDialer
	addr      string
	cfg       *ssh.ClientConfig
	keepAlive time.Duration
	timeout   time.Duration

	mu      sync.Mutex
	client  *ssh.Client
	done    chan struct{}
	opening chan struct{}
	openErr error
	closed  bool
}

func newTunnel(base contextDialer, addr string, cfg *ssh.ClientConfig, keepAlive, timeout time.Duration) *tunnel {
	if keepAlive == 0 {
		keepAlive = 30 * time.Second
	}
	return &tunnel{base: base, addr: addr, cfg: cfg, keepAlive: keepAlive, timeout: timeout}
}

func (t *tunnel) session(ctx context.Context) (*ssh.Client, error) {
	for {
		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			return nil, ErrClosed
		}
		if t.client != nil && !isDone(t.done) {
			c := t.client
			t.mu.Unlock()
			return c, nil
		}
		if wait := t.opening; wait != nil {
			t.mu.Unlock()
			select {
			case <-wait:
				t.mu.Lock()
				err := t.openErr
				t.mu.Unlock()
				if err != nil && !isContextErr(err) {
					return nil, err
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		opening := make(chan struct{})
		t.opening, t.client, t.openErr = opening, nil, nil
		t.mu.Unlock()

		client, done, err := t.open(ctx)

		t.mu.Lock()
		if err == nil && t.closed {
			_ = client.Close()
			err = ErrClosed
		}
		if err == nil {
			t.client, t.done = client, done
		}
		t.openErr, t.opening = err, nil
		close(opening)
		t.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return client, nil
	}
}

func (t *tunnel) open(ctx context.Context) (*ssh.Client, chan struct{}, error) {
	conn, err := t.base.DialContext(ctx, "tcp", t.addr)
	if err != nil {
		return nil, nil, fmt.Errorf("netx: ssh dial %s: %w", t.addr, err)
	}
	deadline := time.Now().Add(t.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	c, chans, reqs, err := ssh.NewClientConn(conn, t.addr, t.cfg)
	if !stop() && err == nil {
		_ = c.Close()
		c, err = nil, ctx.Err()
	}
	if err != nil {
		_ = conn.Close()
		var hk *HostKeyError
		if errors.As(err, &hk) {
			return nil, nil, hk
		}
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, fmt.Errorf("netx: ssh handshake %s: %w", t.addr, err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	done := make(chan struct{})
	go func() {
		_ = client.Wait()
		close(done)
	}()
	if t.keepAlive > 0 {
		go keepAlive(client, done, t.keepAlive, t.timeout)
	}
	return client, done, nil
}

func keepAlive(c *ssh.Client, done <-chan struct{}, every, timeout time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
			answered := make(chan error, 1)
			go func() {
				_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
				answered <- err
			}()
			select {
			case err := <-answered:
				if err != nil {
					_ = c.Close()
					return
				}
			case <-time.After(timeout):
				_ = c.Close()
				return
			case <-done:
				return
			}
		}
	}
}

func (t *tunnel) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	for attempt := 0; ; attempt++ {
		client, err := t.session(ctx)
		if err != nil {
			return nil, err
		}
		dctx, cancel := context.WithTimeout(ctx, t.timeout)
		conn, err := client.DialContext(dctx, network, addr)
		cancel()
		if err == nil {
			return newTunnelConn(conn, network, addr), nil
		}
		var refused *ssh.OpenChannelError
		if errors.As(err, &refused) || ctx.Err() != nil || attempt > 0 {
			return nil, fmt.Errorf("netx: through ssh %s to %s: %w", t.addr, addr, err)
		}
		t.drop(client)
	}
}

func (t *tunnel) drop(client *ssh.Client) {
	t.mu.Lock()
	if t.client == client {
		t.client = nil
	}
	t.mu.Unlock()
	_ = client.Close()
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func isDone(ch chan struct{}) bool {
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (t *tunnel) close() error {
	t.mu.Lock()
	t.closed = true
	client := t.client
	t.client = nil
	t.mu.Unlock()
	if client == nil {
		return nil
	}
	return client.Close()
}
