package llm

import (
	"context"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"
)

func reply(code int, header map[string]string) *http.Response {
	rec := httptest.NewRecorder()
	for k, v := range header {
		rec.Header().Set(k, v)
	}
	rec.WriteHeader(code)
	return rec.Result()
}

func TestRetryOnlyOnWhatAnotherTryCanFix(t *testing.T) {
	fast := RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: 2 * time.Millisecond}
	for _, tc := range []struct {
		name  string
		code  int
		err   error
		tries int
	}{
		{"a rate limit", http.StatusTooManyRequests, nil, 3},
		{"a gateway having a bad minute", http.StatusBadGateway, nil, 3},
		{"overloaded, which net/http has no name for", 529, nil, 3},
		{"a server error", http.StatusInternalServerError, nil, 3},
		{"the request itself being wrong", http.StatusBadRequest, nil, 1},
		{"credentials rejected", http.StatusUnauthorized, nil, 1},
		{"a model that is not there", http.StatusNotFound, nil, 1},
		{"nothing answered at all", 0, &ProviderError{Provider: "p", Message: "dial tcp: connection refused", Kind: ErrUnavailable}, 3},
		{"a request the driver refused to send", 0, errors.New("refusing to send an API key over plain http"), 1},
		{"the caller giving up", 0, context.Canceled, 1},
		// What a driver hands over for a header wait that ran out. Retrying it
		// spends another full wait, three times over.
		{"a wait that already ran out", 0, &ProviderError{Provider: "p", Message: "timeout awaiting response headers", Kind: ErrUnavailable, Err: &url.Error{Op: "Post", Err: os.ErrDeadlineExceeded}}, 1},
		{"a connection nobody accepted", 0, &ProviderError{Provider: "p", Message: "connect: connection refused", Kind: ErrUnavailable, Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}}, 3},
		{"an answer the caller can use", http.StatusOK, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tries := 0
			resp, err := Retry(context.Background(), fast, func() (*http.Response, error) {
				tries++
				if tc.err != nil {
					return nil, tc.err
				}
				return reply(tc.code, nil), nil
			})
			if tries != tc.tries {
				t.Errorf("sent %d times, want %d", tries, tc.tries)
			}
			if tc.err == nil && (resp == nil || resp.StatusCode != tc.code) {
				t.Errorf("resp = %v, err = %v", resp, err)
			}
		})
	}
}

// The parser alone. What Retry does with the answer is the two tests below it,
// which are the ones that go red when the override is deleted.
func TestRetryAfterIsRead(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
		ok     bool
	}{
		{"2", 2 * time.Second, true},
		{"0", 0, true},
		{"-5", 0, true},
		{"", 0, false},
		{"not a number", 0, false},
		{time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), 0, true},
	} {
		got, ok := retryAfter(reply(429, map[string]string{"Retry-After": tc.header}))
		if ok != tc.ok || got != tc.want {
			t.Errorf("Retry-After %q -> %v %v, want %v %v", tc.header, got, ok, tc.want, tc.ok)
		}
	}
	// A date far enough ahead to be unambiguous, rounded because the parse
	// loses the sub-second part.
	got, ok := retryAfter(reply(429, map[string]string{"Retry-After": time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)}))
	if !ok || got < 80*time.Second || got > 90*time.Second {
		t.Errorf("an HTTP date gave %v %v", got, ok)
	}
}

// The wait is where a retry spends its time, so the caller's ctx has to end it.
func TestRetryStopsWhenTheCallerGivesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tries := 0
	start := time.Now()
	_, err := Retry(ctx, RetryPolicy{Attempts: 5, Base: time.Hour, Max: time.Hour}, func() (*http.Response, error) {
		tries++
		cancel()
		return reply(http.StatusTooManyRequests, nil), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if tries != 1 || time.Since(start) > 5*time.Second {
		t.Errorf("sent %d times in %v", tries, time.Since(start))
	}
}

func TestAPolicyThatRetriesNothingSendsOnce(t *testing.T) {
	tries := 0
	if _, err := Retry(context.Background(), RetryPolicy{}, func() (*http.Response, error) {
		tries++
		return reply(http.StatusTooManyRequests, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	if tries != 1 {
		t.Errorf("sent %d times", tries)
	}
}

// Exact, which the noise made impossible to say until it moved into jitter().
func TestTheBackoffDoublesUpToTheCeiling(t *testing.T) {
	pol := RetryPolicy{Attempts: 9, Base: time.Second, Max: 4 * time.Second}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	for i, w := range want {
		if d := backoff(pol, i+1); d != w {
			t.Errorf("attempt %d waited %v, want %v", i+1, d, w)
		}
	}
}

// The noise is what keeps several clients that hit one rate limit from coming
// back together, so it has to be there and it has to stay under the ceiling.
func TestTheNoiseStaysInsideTheWait(t *testing.T) {
	const d = 4 * time.Second
	seen := map[time.Duration]bool{}
	for range 200 {
		got := jitter(d)
		if got < d/2 || got > d {
			t.Fatalf("jitter(%v) = %v, outside [%v, %v]", d, got, d/2, d)
		}
		seen[got] = true
	}
	if len(seen) < 100 {
		t.Errorf("200 waits took %d different values; that is not noise", len(seen))
	}
}

// Policies a caller can write that used to end in a panic on the error path, or
// in a negative wait, which fires at once and is a hot loop with nothing to see.
func TestTheBackoffNeverPanicsOrGoesBackwards(t *testing.T) {
	for _, pol := range []RetryPolicy{
		{Attempts: 40, Base: time.Second, Max: math.MaxInt64},
		{Attempts: 8, Base: -time.Second, Max: time.Second},
		{Attempts: 8, Base: time.Second, Max: -1},
		{Attempts: 8},
	} {
		for attempt := 1; attempt <= pol.Attempts; attempt++ {
			if d := jitter(backoff(pol, attempt)); d < 0 {
				t.Fatalf("%+v attempt %d waited %v", pol, attempt, d)
			}
		}
	}
}

// Measured, not read: a Retry-After longer than the backoff has to be the wait
// that actually happens, or deleting the override changes nothing.
func TestTheServerNamesTheWait(t *testing.T) {
	tries := 0
	start := time.Now()
	if _, err := Retry(context.Background(), RetryPolicy{Attempts: 2, Base: time.Millisecond, Max: time.Minute}, func() (*http.Response, error) {
		tries++
		return reply(http.StatusTooManyRequests, map[string]string{"Retry-After": "1"}), nil
	}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); tries != 2 || d < time.Second {
		t.Errorf("sent %d times in %v; the server asked for a second", tries, d)
	}
}

// Max is the patience as well as the ceiling: coming back before the server
// allowed spends a request on the same refusal.
func TestAWaitLongerThanThePolicyGoesToTheCaller(t *testing.T) {
	tries := 0
	start := time.Now()
	resp, err := Retry(context.Background(), RetryPolicy{Attempts: 3, Base: time.Millisecond, Max: 20 * time.Millisecond}, func() (*http.Response, error) {
		tries++
		return reply(http.StatusTooManyRequests, map[string]string{"Retry-After": "60"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if tries != 1 || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("sent %d times, resp = %v", tries, resp)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("waited %v for a minute-long Retry-After", d)
	}
}

// A policy naming only Attempts must not turn one 429 into three requests in
// the same millisecond, which is what an uncapped zero Base did.
func TestAPolicyWithNoTimingStillWaits(t *testing.T) {
	tries := 0
	start := time.Now()
	if _, err := Retry(context.Background(), RetryPolicy{Attempts: 3}, func() (*http.Response, error) {
		tries++
		return reply(http.StatusTooManyRequests, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); tries != 3 || d < time.Second {
		t.Errorf("sent %d times in %v", tries, d)
	}
}
