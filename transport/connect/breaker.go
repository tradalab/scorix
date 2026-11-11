package connect

import (
	"context"
	"sync"
	"time"

	"connectrpc.com/connect"
)

const (
	bucketSpan = 250 * time.Millisecond
	buckets    = 40
	tolerance  = 1.5

	ownDeadlineSlack = 10 * time.Millisecond
)

type bucket struct {
	start    int64
	total    int
	accepted int
}

type breaker struct {
	mu   sync.Mutex
	now  func() time.Time
	roll func() float64
	ring [buckets]bucket
}

func (b *breaker) counts(at int64) (total, accepted int) {
	for _, k := range b.ring {
		if at-k.start < buckets {
			total += k.total
			accepted += k.accepted
		}
	}
	return total, accepted
}

func (b *breaker) allow() bool {
	b.mu.Lock()
	total, accepted := b.counts(b.now().UnixNano() / int64(bucketSpan))
	b.mu.Unlock()
	reject := (float64(total) - tolerance*float64(accepted)) / float64(total+1)
	return b.roll() >= reject
}

func (b *breaker) record(err error) {
	at := b.now().UnixNano() / int64(bucketSpan)
	b.mu.Lock()
	defer b.mu.Unlock()
	k := &b.ring[at%buckets]
	if k.start != at {
		*k = bucket{start: at}
	}
	k.total++
	if !calleeFailed(err) {
		k.accepted++
	}
}

func (b *breaker) settle(ctx context.Context, err error) {
	if d, ok := ctx.Deadline(); ok && time.Until(d) < ownDeadlineSlack && connect.CodeOf(err) == connect.CodeDeadlineExceeded {
		return
	}
	b.record(err)
}

func calleeFailed(err error) bool {
	if err == nil {
		return false
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnknown, connect.CodeDeadlineExceeded, connect.CodeUnimplemented,
		connect.CodeInternal, connect.CodeUnavailable, connect.CodeDataLoss:
		return true
	}
	return false
}
