// Package redis opens a go-redis client from a service's config and proves it answers before
// the service starts serving.
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

// Open fails on a Host that is not host:port, and when the server does not answer PING
// within ctx and Timeout; the error names Host, never pass.
func Open(ctx context.Context, cfg Config, pass string) (*Client, error) {
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
	c := goredis.NewClient(opt)
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		c.Close()
		return nil, fmt.Errorf("redis: ping %s: %w", cfg.Host, err)
	}
	return c, nil
}
