// Package nats opens a NATS connection from a service's config and proves the server answers
// before the service starts serving.
package nats

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	natsgo "github.com/nats-io/nats.go"
)

type Conn = natsgo.Conn

type Msg = natsgo.Msg

// Config is the YAML side of a connection. Url lists one or more servers, comma separated,
// each nats:// or tls://. The password or token is not in it: it goes to Open.
type Config struct {
	Url     string
	User    string        `conf:"optional"`
	Tls     bool          `conf:"optional"`
	Name    string        `conf:"optional"`
	Timeout time.Duration `conf:"default=3s"`
}

// Open sends pass as the password of User, or as a token when User is empty. It fails on a
// Url that carries credentials, and when no server completes the handshake within ctx and
// Timeout; the error names Url, never pass.
func Open(ctx context.Context, cfg Config, pass string) (*Conn, error) {
	if err := check(cfg); err != nil {
		return nil, err
	}
	wait := cfg.Timeout
	if dl, ok := ctx.Deadline(); ok {
		wait = min(wait, time.Until(dl))
	}
	if err := ctx.Err(); err != nil || wait <= 0 {
		return nil, fmt.Errorf("nats: connect %s: %w", cfg.Url, context.DeadlineExceeded)
	}
	opts := []natsgo.Option{natsgo.Timeout(wait), natsgo.Name(cfg.Name)}
	switch {
	case cfg.User != "":
		opts = append(opts, natsgo.UserInfo(cfg.User, pass))
	default:
		opts = append(opts, natsgo.Token(pass))
	}
	if cfg.Tls {
		opts = append(opts, natsgo.Secure(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	type dialed struct {
		nc  *Conn
		err error
	}
	done := make(chan dialed, 1)
	go func() {
		nc, err := natsgo.Connect(cfg.Url, opts...)
		done <- dialed{nc, err}
	}()
	select {
	case d := <-done:
		if d.err != nil {
			return nil, fmt.Errorf("nats: connect %s: %w", cfg.Url, d.err)
		}
		return d.nc, nil
	case <-ctx.Done():
		go func() {
			if d := <-done; d.nc != nil {
				d.nc.Close()
			}
		}()
		return nil, fmt.Errorf("nats: connect %s: %w", cfg.Url, ctx.Err())
	}
}

func check(cfg Config) error {
	if cfg.Timeout <= 0 {
		return errors.New("nats: Timeout is required")
	}
	if strings.TrimSpace(cfg.Url) == "" {
		return errors.New("nats: Url is required")
	}
	for i, s := range strings.Split(cfg.Url, ",") {
		u, err := url.Parse(strings.TrimSpace(s))
		switch {
		case strings.Contains(s, "@"):
			return errors.New("nats: Url must not carry credentials; pass them to Open")
		case err != nil:
			return fmt.Errorf("nats: server %d in Url is not a URL", i+1)
		case (u.Scheme != "nats" && u.Scheme != "tls") || u.Hostname() == "":
			return fmt.Errorf("nats: server %d in Url is not nats://host:port or tls://host:port", i+1)
		}
	}
	return nil
}
