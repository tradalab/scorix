// Package kafka opens a franz-go client from a service's config and proves a broker answers
// before the service starts serving.
package kafka

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
)

type Client = kgo.Client

type Record = kgo.Record

type Opt = kgo.Opt

// Config is the YAML side of a connection. Mechanism is PLAIN, SCRAM-SHA-256 or
// SCRAM-SHA-512 and needs User; the password is not in it: it goes to Open.
type Config struct {
	Brokers   []string
	User      string        `conf:"optional"`
	Mechanism string        `conf:"optional"`
	Tls       bool          `conf:"optional"`
	ClientID  string        `conf:"optional"`
	Timeout   time.Duration `conf:"default=10s"`
}

// Open passes opts (topics, group, producer settings) after the connection's own. It fails
// on a broker that is not host:port, and when no broker answers within ctx and Timeout;
// the error names Brokers, never pass.
func Open(ctx context.Context, cfg Config, pass string, opts ...Opt) (*Client, error) {
	mech, err := check(cfg, pass)
	if err != nil {
		return nil, err
	}
	all := []Opt{kgo.SeedBrokers(cfg.Brokers...)}
	if cfg.ClientID != "" {
		all = append(all, kgo.ClientID(cfg.ClientID))
	}
	if mech != nil {
		all = append(all, kgo.SASL(mech))
	}
	if cfg.Tls {
		all = append(all, kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}))
	}
	cl, err := kgo.NewClient(append(all, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	ping, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cl.Ping(ping) }()
	select {
	case err = <-done:
	case <-ping.Done():
		err = ping.Err()
	}
	if err != nil {
		cl.Close()
		return nil, fmt.Errorf("kafka: ping %s: %w", strings.Join(cfg.Brokers, ","), err)
	}
	return cl, nil
}

func check(cfg Config, pass string) (sasl.Mechanism, error) {
	if cfg.Timeout <= 0 {
		return nil, errors.New("kafka: Timeout is required")
	}
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("kafka: Brokers is required")
	}
	for _, b := range cfg.Brokers {
		if _, _, err := net.SplitHostPort(b); err != nil {
			return nil, fmt.Errorf("kafka: broker %q is not host:port", b)
		}
	}
	if (cfg.Mechanism == "") != (cfg.User == "") {
		return nil, errors.New("kafka: User and Mechanism go together")
	}
	switch cfg.Mechanism {
	case "":
		return nil, nil
	case "PLAIN":
		return plain.Auth{User: cfg.User, Pass: pass}.AsMechanism(), nil
	case "SCRAM-SHA-256":
		return scram.Auth{User: cfg.User, Pass: pass}.AsSha256Mechanism(), nil
	case "SCRAM-SHA-512":
		return scram.Auth{User: cfg.User, Pass: pass}.AsSha512Mechanism(), nil
	}
	return nil, fmt.Errorf("kafka: Mechanism %q is not PLAIN, SCRAM-SHA-256 or SCRAM-SHA-512", cfg.Mechanism)
}
