package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Mirrors serves one file from hosts holding the same bytes: each open tries the host that
// last answered first and moves on, keeping the offset, so the next host finishes the part.
// Give the File a Digest; it is what catches a host whose bytes differ.
func Mirrors(hosts ...Opener) Opener {
	m := &mirrors{hosts: hosts}
	return m.open
}

type mirrors struct {
	hosts []Opener
	mu    sync.Mutex
	cur   int
}

type allHosts struct{ errs []error }

func (a *allHosts) Error() string {
	parts := make([]string, len(a.errs))
	for i, err := range a.errs {
		parts[i] = err.Error()
	}
	return fmt.Sprintf("fetch: no host served it (%s)", strings.Join(parts, "; "))
}

func (m *mirrors) open(ctx context.Context, offset int64) (*http.Response, error) {
	if len(m.hosts) == 0 {
		return nil, Permanent(errors.New("fetch: no host to fetch from"))
	}
	m.mu.Lock()
	start := m.cur
	m.mu.Unlock()
	errs := make([]error, 0, len(m.hosts))
	settled := true
	for i := range m.hosts {
		idx := (start + i) % len(m.hosts)
		resp, err := m.hosts[idx](ctx, offset)
		if err == nil && servesFile(resp.StatusCode, offset) {
			m.mu.Lock()
			m.cur = idx
			m.mu.Unlock()
			return resp, nil
		}
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			err = &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !refusedForGood(err) {
			settled = false
		}
		errs = append(errs, err)
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	if settled {
		return nil, Permanent(errors.Join(errs...))
	}
	return nil, &allHosts{errs: errs}
}

func servesFile(code int, offset int64) bool {
	return code == http.StatusOK || code == http.StatusPartialContent ||
		(code == http.StatusRequestedRangeNotSatisfiable && offset > 0)
}

func refusedForGood(err error) bool {
	var perm permanent
	if errors.As(err, &perm) {
		return true
	}
	var se *StatusError
	return errors.As(err, &se) && !se.retryable()
}
