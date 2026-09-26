package llm

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

// How many times a request is sent again, and how long to wait between tries.
// An unset Base or Max takes DefaultRetry's: a policy naming only Attempts means
// "retry, timing up to you", and a zero Base would send every try at once.
type RetryPolicy struct {
	// Total tries including the first. Below 2 is no retry.
	Attempts int
	// The first wait, doubled each try and capped at Max.
	Base time.Duration
	// The ceiling on one wait, and with it the patience: a Retry-After longer
	// than this is believed rather than argued with, and the answer goes to the
	// caller instead.
	Max time.Duration
	// The only trace a retry leaves: without it a chat that waited twenty
	// seconds and then worked looks like a slow server. Runs on the request's
	// goroutine before the wait, so it must not block. A driver filling in the
	// numbers of an unset policy must keep this field, or the one caller that
	// sets it - Driver.Open - never sees it fire.
	OnRetry func(attempt int, wait time.Duration, status int, err error)
}

// What a hosted API's rate limit and a gateway's bad minute both need. Three
// tries and at most ~20 seconds of waiting: long enough to ride out a 429,
// short enough that a person watching a spinner does not give up first.
var DefaultRetry = RetryPolicy{Attempts: 3, Base: time.Second, Max: 20 * time.Second}

// Retry sends until the answer is one the caller can act on, the context ends,
// or the policy runs out. send must build a fresh request each time - a body is
// read once - and must return a nil response with an error, because callers
// return on the error without closing the body. Nothing retries after the
// response has started, and the context ending comes back as ctx.Err() alone so
// a caller can tell its own cancel from a refusal.
func Retry(ctx context.Context, pol RetryPolicy, send func() (*http.Response, error)) (*http.Response, error) {
	if pol.Attempts < 2 {
		return send()
	}
	if pol.Base <= 0 {
		pol.Base = DefaultRetry.Base
	}
	if pol.Max <= 0 {
		pol.Max = DefaultRetry.Max
	}
	for attempt := 1; ; attempt++ {
		resp, err := send()
		if attempt >= pol.Attempts || !worthRetrying(resp, err) {
			return resp, err
		}
		wait := jitter(backoff(pol, attempt))
		if resp != nil {
			if after, ok := retryAfter(resp); ok {
				if after > pol.Max {
					return resp, err
				}
				wait = after
			}
			// Unread: net/http drains a small closed-early body itself, and
			// reading it here would pull in a proxy's error page before every
			// retry.
			resp.Body.Close()
		}
		if pol.OnRetry != nil {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			pol.OnRetry(attempt, wait, status, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func worthRetrying(resp *http.Response, err error) bool {
	if err != nil {
		// A deadline is not a bad moment: another try costs another full wait,
		// three times 90s for one chat and the audio uploaded three times for
		// Transcribe.
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return false
		}
		// Only what the transport could not do. A request the driver refused to
		// send will not send any better the second time.
		return errors.Is(err, ErrUnavailable)
	}
	switch resp.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	// Anthropic's own code for "overloaded", which is not in net/http.
	return resp.StatusCode == 529
}

// Seconds, or an HTTP date, per RFC 9110. A server that names a moment already
// in the past means "now".
func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, true
		}
		return time.Duration(secs) * time.Second, true
	}
	if when, err := http.ParseTime(v); err == nil {
		if d := time.Until(when); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// Doubling up to the ceiling, kept apart from the noise below: clamping a
// jittered wait makes every client at the ceiling agree on exactly Max.
func backoff(pol RetryPolicy, attempt int) time.Duration {
	d := pol.Base
	for range attempt - 1 {
		// Tested before doubling: d*2 overflows into a negative duration for a
		// Max near the end of the range, and a negative wait fires at once.
		if d > pol.Max/2 {
			d = pol.Max
			break
		}
		d *= 2
	}
	if d > pol.Max {
		d = pol.Max
	}
	if d < 0 {
		return 0
	}
	return d
}

// Somewhere in the second half of the wait, so requests that hit one rate limit
// together do not come back together. Downward, so the ceiling holds.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	return d - half + rand.N(half+1)
}
