// Package redis opens a go-redis client from a service's config without dialing, so a Redis that
// is down during a deploy does not stop the service; Ping proves it answers when a caller needs that.
package redis

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

type Client = goredis.Client

type Script = goredis.Script

// Nil is the error a read returns for a key that does not exist.
var Nil = goredis.Nil

type Z = goredis.Z

type ZRangeBy = goredis.ZRangeBy

func NewScript(src string) *Script { return goredis.NewScript(src) }

// Config is the YAML side of a connection. The password is not in it: it comes from the
// service's own secret env var and goes to Open, so the file can never carry it.
type Config struct {
	Host     string
	User     string        `conf:"optional"`
	DB       int           `conf:"optional"`
	Tls      bool          `conf:"optional"`
	PoolSize int           `conf:"optional"`
	Timeout  time.Duration `conf:"default=3s"`
}

// Open fails only on a bad Config and dials nothing: the client connects on first use and
// reconnects by itself. ctx is unused; call Ping to fail fast or to answer a readiness probe.
func Open(_ context.Context, cfg Config, pass string) (*Client, error) {
	host, _, err := net.SplitHostPort(cfg.Host)
	switch {
	case err != nil:
		return nil, fmt.Errorf("redis: Host %q is not host:port", cfg.Host)
	case cfg.DB < 0:
		return nil, errors.New("redis: DB must not be negative")
	case cfg.PoolSize < 0:
		return nil, errors.New("redis: PoolSize must not be negative")
	case cfg.Timeout <= 0:
		return nil, errors.New("redis: Timeout is required")
	}
	opt := &goredis.Options{
		Addr:         cfg.Host,
		Username:     cfg.User,
		Password:     pass,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		DialTimeout:  cfg.Timeout,
		ReadTimeout:  cfg.Timeout,
		WriteTimeout: cfg.Timeout,

		ContextTimeoutEnabled: true,
	}
	if cfg.Tls {
		opt.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	}
	return goredis.NewClient(opt), nil
}

// Ping fails when the server does not answer PING within ctx and the Config's Timeout; the
// error names Host, never the password. It leaves c open either way.
func Ping(ctx context.Context, c *Client) error {
	o := c.Options()
	ctx, cancel := context.WithTimeout(ctx, o.DialTimeout)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis: ping %s: %w", o.Addr, err)
	}
	return nil
}
