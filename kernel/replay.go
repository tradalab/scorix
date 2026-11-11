package kernel

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrInFlight is Claim's answer while another call holds the key and has not finished.
var ErrInFlight = errors.New("a call with this idempotency key is still running")

// Replayed is the outcome kept for a key: the request's Sum, then either the reply's
// bytes or the Code and Message of the error the caller got.
type Replayed struct {
	Sum     [32]byte
	Reply   []byte
	Code    int32
	Message string
}

// ReplayStore keeps the first outcome of each idempotency key. Claim returns a token when the caller
// owns key, the kept outcome, or ErrInFlight; the owner hands the token to Keep with its outcome or to
// Drop so a retry runs again, and both do nothing once another caller has claimed the key.
type ReplayStore interface {
	Claim(ctx context.Context, key string, sum [32]byte) (token string, prior *Replayed, err error)
	Keep(ctx context.Context, key, token string, r Replayed) error
	Drop(ctx context.Context, key, token string) error
}

type replayEntry struct {
	token   string
	done    bool
	r       Replayed
	expires time.Time
}

type memoryReplay struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]*replayEntry
	prune   time.Time
}

// NewMemoryReplay keeps outcomes in this process for ttl, so a retry reaches the same
// replica; a claim never finished frees its key after ttl too.
func NewMemoryReplay(ttl time.Duration) (ReplayStore, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("kernel: replay ttl %v is not positive", ttl)
	}
	return &memoryReplay{ttl: ttl, now: time.Now, entries: map[string]*replayEntry{}}, nil
}

func (m *memoryReplay) Claim(_ context.Context, key string, sum [32]byte) (string, *Replayed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if now.After(m.prune) {
		for k, e := range m.entries {
			if now.After(e.expires) {
				delete(m.entries, k)
			}
		}
		m.prune = now.Add(m.ttl)
	}
	if e, ok := m.entries[key]; ok && !now.After(e.expires) {
		if !e.done {
			return "", nil, ErrInFlight
		}
		r := e.r
		return "", &r, nil
	}
	token := rand.Text()
	m.entries[key] = &replayEntry{token: token, r: Replayed{Sum: sum}, expires: now.Add(m.ttl)}
	return token, nil, nil
}

func (m *memoryReplay) Keep(_ context.Context, key, token string, r Replayed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && e.token == token {
		m.entries[key] = &replayEntry{token: token, done: true, r: r, expires: m.now().Add(m.ttl)}
	}
	return nil
}

func (m *memoryReplay) Drop(_ context.Context, key, token string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok && e.token == token {
		delete(m.entries, key)
	}
	return nil
}
