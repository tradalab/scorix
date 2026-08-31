package updater

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tradalab/scorix/logger"
)

type chainProvider struct { // the manifest URLs are baked into the binary, so the only cure for a dead host is a second one shipped alongside it
	providers []UpdateProvider
	labels    []string
	timeout   time.Duration
}

func newChainProvider(providers []UpdateProvider, labels []string) *chainProvider {
	return &chainProvider{providers: providers, labels: labels, timeout: perSourceTimeout}
}

const perSourceTimeout = 8 * time.Second // bounds the manifest fetch only: the check runs at startup, and a dead first host must not burn the client's 30s before the second is tried

func (c *chainProvider) CheckForUpdate(ctx context.Context, currentVersion, platformKey string) (*Result, error) {
	var errs []error
	for i, p := range c.providers {
		if err := ctx.Err(); err != nil { // a caller that gave up must not wait on the remaining sources
			return nil, errors.Join(append(errs, err)...)
		}
		res, err := c.try(ctx, p, currentVersion, platformKey)
		if err == nil {
			if i > 0 {
				logger.Warn(fmt.Sprintf("[updater] %s did not answer; used fallback %s", c.label(0), c.label(i)))
			}
			return res, nil // first answer wins, never the newest across sources: that would let any one source override the rest
		}
		if errors.Is(err, ErrNoUpdate) { // "up to date" is an answer: moving on would let a stale mirror offer a version the primary retired
			return res, err
		}
		errs = append(errs, fmt.Errorf("%s: %w", c.label(i), err))
	}
	return nil, errors.Join(errs...)
}

func (c *chainProvider) try(ctx context.Context, p UpdateProvider, currentVersion, platformKey string) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return p.CheckForUpdate(ctx, currentVersion, platformKey)
}

func (c *chainProvider) label(i int) string {
	if i < len(c.labels) && c.labels[i] != "" {
		return c.labels[i]
	}
	return fmt.Sprintf("source %d", i+1)
}
