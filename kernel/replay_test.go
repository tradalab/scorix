package kernel

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func memory(t *testing.T, ttl time.Duration) (*memoryReplay, *time.Time) {
	t.Helper()
	s, err := NewMemoryReplay(ttl)
	if err != nil {
		t.Fatal(err)
	}
	m := s.(*memoryReplay)
	now := time.Unix(1000, 0)
	m.now = func() time.Time { return now }
	return m, &now
}

func TestTheFirstClaimOwnsAKeyAndEveryOtherWaitsOrReplays(t *testing.T) {
	ctx := context.Background()
	s, _ := memory(t, time.Hour)
	sum := [32]byte{1}
	token, prior, err := s.Claim(ctx, "k", sum)
	if token == "" || prior != nil || err != nil {
		t.Fatalf("first claim = %q, %v, %v", token, prior, err)
	}
	if _, _, err := s.Claim(ctx, "k", sum); !errors.Is(err, ErrInFlight) {
		t.Fatalf("a claim while the first runs = %v, want ErrInFlight", err)
	}
	if err := s.Keep(ctx, "k", token, Replayed{Sum: sum, Reply: []byte("r")}); err != nil {
		t.Fatal(err)
	}
	if again, prior, err := s.Claim(ctx, "k", sum); err != nil || again != "" || prior == nil || string(prior.Reply) != "r" || prior.Sum != sum {
		t.Fatalf("a claim after the first finished = %q, %+v, %v", again, prior, err)
	}
	if other, prior, err := s.Claim(ctx, "other", sum); other == "" || other == token || prior != nil || err != nil {
		t.Fatalf("another key = %q, %v, %v", other, prior, err)
	}
}

func TestADroppedKeyCanBeClaimedAgain(t *testing.T) {
	ctx := context.Background()
	s, _ := memory(t, time.Hour)
	token, _, _ := s.Claim(ctx, "k", [32]byte{})
	s.Drop(ctx, "k", token)
	if token, prior, err := s.Claim(ctx, "k", [32]byte{}); token == "" || prior != nil || err != nil {
		t.Fatalf("claim after drop = %q, %v, %v", token, prior, err)
	}
}

func TestAnOwnerPastItsTTLCanNeitherKeepNorDropTheNextOwnersClaim(t *testing.T) {
	ctx := context.Background()
	s, now := memory(t, time.Minute)
	late, _, _ := s.Claim(ctx, "k", [32]byte{1})
	*now = now.Add(2 * time.Minute)
	next, _, err := s.Claim(ctx, "k", [32]byte{2})
	if next == "" || err != nil {
		t.Fatalf("a claim after the first expired = %q, %v", next, err)
	}
	s.Keep(ctx, "k", late, Replayed{Sum: [32]byte{1}, Reply: []byte("late")})
	s.Drop(ctx, "k", late)
	if _, _, err := s.Claim(ctx, "k", [32]byte{2}); !errors.Is(err, ErrInFlight) {
		t.Fatalf("the next owner's claim after the late keep and drop = %v, want ErrInFlight", err)
	}
	s.Keep(ctx, "k", next, Replayed{Sum: [32]byte{2}, Reply: []byte("next")})
	if _, prior, err := s.Claim(ctx, "k", [32]byte{2}); err != nil || prior == nil || string(prior.Reply) != "next" {
		t.Fatalf("the kept outcome = %+v, %v", prior, err)
	}
}

func TestAKeptReplyIsForgottenAfterItsTTL(t *testing.T) {
	ctx := context.Background()
	s, now := memory(t, time.Minute)
	token, _, _ := s.Claim(ctx, "k", [32]byte{})
	s.Keep(ctx, "k", token, Replayed{Reply: []byte("r")})
	*now = now.Add(59 * time.Second)
	if _, prior, _ := s.Claim(ctx, "k", [32]byte{}); prior == nil {
		t.Fatal("forgotten before its TTL")
	}
	*now = now.Add(2 * time.Second)
	if token, prior, err := s.Claim(ctx, "k", [32]byte{}); token == "" || prior != nil || err != nil {
		t.Fatalf("kept past its TTL: %q, %v, %v", token, prior, err)
	}
	for i := range 100 {
		k := string(rune('a' + i))
		token, _, _ := s.Claim(ctx, k, [32]byte{})
		s.Keep(ctx, k, token, Replayed{})
	}
	*now = now.Add(2 * time.Minute)
	s.Claim(ctx, "trigger", [32]byte{})
	if n := len(s.entries); n != 1 {
		t.Fatalf("%d entries survive their TTL", n)
	}
}

func TestAClaimNobodyFinishesExpiresSoACrashedCallDoesNotLockItsKey(t *testing.T) {
	ctx := context.Background()
	s, now := memory(t, time.Minute)
	s.Claim(ctx, "k", [32]byte{})
	*now = now.Add(2 * time.Minute)
	if token, prior, err := s.Claim(ctx, "k", [32]byte{}); token == "" || prior != nil || err != nil {
		t.Fatalf("an abandoned claim still held its key: %q, %v, %v", token, prior, err)
	}
}

func TestAKeyExpiresOnItsOwnClockNotTheSweepers(t *testing.T) {
	ctx := context.Background()
	s, now := memory(t, time.Minute)
	s.Claim(ctx, "sweep", [32]byte{})
	*now = now.Add(50 * time.Second)
	token, _, _ := s.Claim(ctx, "k", [32]byte{})
	s.Keep(ctx, "k", token, Replayed{Reply: []byte("r")})
	*now = now.Add(11 * time.Second)
	s.Claim(ctx, "sweep2", [32]byte{})
	*now = now.Add(55 * time.Second)
	if token, prior, err := s.Claim(ctx, "k", [32]byte{}); token == "" || prior != nil || err != nil {
		t.Fatalf("a key past its TTL between sweeps = %q, %v, %v", token, prior, err)
	}
}

func TestAMemoryReplayWithoutAPositiveTTLIsRefused(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := NewMemoryReplay(ttl); err == nil || !strings.Contains(err.Error(), "ttl") {
			t.Errorf("ttl %v = %v", ttl, err)
		}
	}
}
