package updater

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type chainSource struct {
	res    *Result
	err    error
	block  time.Duration
	called int
}

func (f *chainSource) CheckForUpdate(ctx context.Context, _, _ string) (*Result, error) {
	f.called++
	if f.block > 0 {
		select {
		case <-time.After(f.block):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.res, f.err
}

func chainOf(p ...UpdateProvider) *chainProvider {
	labels := make([]string, len(p))
	for i := range p {
		labels[i] = string(rune('A' + i))
	}
	c := newChainProvider(p, labels)
	c.timeout = 40 * time.Millisecond // keeps the bound test off the real 8s
	return c
}

func TestChainUsesFirstAnswer(t *testing.T) {
	first := &chainSource{res: &Result{HasUpdate: true, NewVersion: "1.0.0"}}
	second := &chainSource{res: &Result{HasUpdate: true, NewVersion: "9.9.9"}}

	res, err := chainOf(first, second).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if res.NewVersion != "1.0.0" {
		t.Fatalf("got %s, want the first source's answer", res.NewVersion)
	}
	if second.called != 0 {
		t.Fatal("the fallback ran even though the primary answered")
	}
}

func TestChainFallsThroughOnFailure(t *testing.T) {
	first := &chainSource{err: errors.New("dial tcp: no such host")}
	second := &chainSource{res: &Result{HasUpdate: true, NewVersion: "1.0.0"}}

	res, err := chainOf(first, second).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err != nil {
		t.Fatal(err)
	}
	if res.NewVersion != "1.0.0" || second.called != 1 {
		t.Fatalf("fallback not used: res=%v called=%d", res, second.called)
	}
}

func TestChainFallsThroughOnVerificationFailure(t *testing.T) {
	first := &chainSource{err: errors.New("appcast: manifest signature verification failed (refusing)")} // a reason to try elsewhere: both sources verify against the same embedded key
	second := &chainSource{res: &Result{HasUpdate: true, NewVersion: "1.0.0"}}

	if _, err := chainOf(first, second).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64"); err != nil {
		t.Fatal(err)
	}
	if second.called != 1 {
		t.Fatal("a refused signature stopped the chain instead of moving on")
	}
}

func TestChainStopsOnNoUpdate(t *testing.T) {
	first := &chainSource{res: &Result{HasUpdate: false}, err: ErrNoUpdate}
	second := &chainSource{res: &Result{HasUpdate: true, NewVersion: "9.9.9"}}

	res, err := chainOf(first, second).CheckForUpdate(context.Background(), "1.0.0", "windows-amd64")
	if !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("got %v, want ErrNoUpdate", err)
	}
	if res == nil || res.HasUpdate {
		t.Fatal("HasUpdate should have come back false")
	}
	if second.called != 0 {
		t.Fatal("a stale mirror was consulted after the primary said up to date")
	}
}

func TestChainReportsEverySourceWhenAllFail(t *testing.T) {
	first := &chainSource{err: errors.New("primary down")}
	second := &chainSource{err: errors.New("mirror down")}

	_, err := chainOf(first, second).CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	if err == nil {
		t.Fatal("all sources failed but the chain reported success")
	}
	for _, want := range []string{"primary down", "mirror down", "A", "B"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestChainBoundsEachSource(t *testing.T) {
	c := chainOf(
		&chainSource{block: time.Hour}, // a host that accepts and never answers
		&chainSource{res: &Result{HasUpdate: true, NewVersion: "1.0.0"}},
	)

	started := time.Now()
	res, err := c.CheckForUpdate(context.Background(), "0.1.0", "windows-amd64")
	elapsed := time.Since(started)

	if err != nil {
		t.Fatal(err)
	}
	if res.NewVersion != "1.0.0" {
		t.Fatalf("got %v", res)
	}
	if elapsed >= c.timeout*3 {
		t.Fatalf("waited %s with a %s bound; the per-source timeout did not apply", elapsed, c.timeout)
	}
}

func TestPerSourceTimeoutIsShorterThanTheHTTPClient(t *testing.T) {
	if perSourceTimeout >= defaultClient().Timeout {
		t.Fatalf("perSourceTimeout %s does not bound the %s client timeout", perSourceTimeout, defaultClient().Timeout)
	}
	if newChainProvider(nil, nil).timeout != perSourceTimeout {
		t.Fatal("the constructor does not apply the default timeout")
	}
}

func TestChainHonoursCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	first := &chainSource{block: time.Second}
	second := &chainSource{res: &Result{HasUpdate: true}}

	if _, err := chainOf(first, second).CheckForUpdate(ctx, "0.1.0", "windows-amd64"); err == nil {
		t.Fatal("a cancelled context still produced a result")
	}
}

func TestChainSingleSourceBehavesLikeTheProvider(t *testing.T) {
	only := &chainSource{res: &Result{HasUpdate: true, NewVersion: "2.0.0"}}
	res, err := chainOf(only).CheckForUpdate(context.Background(), "1.0.0", "windows-amd64")
	if err != nil || res.NewVersion != "2.0.0" {
		t.Fatalf("res=%v err=%v", res, err)
	}
}
