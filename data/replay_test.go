package data

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/kernel"
)

const replayDDL = `CREATE TABLE idempotency (
  key        TEXT PRIMARY KEY,
  claim      TEXT NOT NULL,
  sum        TEXT NOT NULL,
  done       BOOLEAN NOT NULL,
  reply      BYTEA,
  code       INTEGER NOT NULL DEFAULT 0,
  message    TEXT NOT NULL DEFAULT '',
  expires_at BIGINT NOT NULL
)`

func replayStores(t *testing.T, n int) ([]*sqlReplay, *time.Time) {
	t.Helper()
	db := open(t, pool)
	if _, err := db.ExecCtx(ctx, replayDDL); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	var out []*sqlReplay
	for range n {
		s, err := NewReplayStore(db, "idempotency", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		r := s.(*sqlReplay)
		r.now = func() time.Time { return now }
		out = append(out, r)
	}
	return out, &now
}

func TestTwoReplicasSeeOneClaimAndOneKeptOutcome(t *testing.T) {
	s, _ := replayStores(t, 2)
	a, b := s[0], s[1]
	sum := [32]byte{7}
	token, prior, err := a.Claim(ctx, "k", sum)
	if token == "" || prior != nil || err != nil {
		t.Fatalf("first claim = %q, %v, %v", token, prior, err)
	}
	if _, _, err := b.Claim(ctx, "k", sum); !errors.Is(err, kernel.ErrInFlight) {
		t.Fatalf("the other replica's claim = %v, want ErrInFlight", err)
	}
	if err := a.Keep(ctx, "k", token, kernel.Replayed{Sum: sum, Reply: []byte{1, 2}, Code: 5, Message: "gone"}); err != nil {
		t.Fatal(err)
	}
	again, prior, err := b.Claim(ctx, "k", sum)
	if err != nil || again != "" || prior == nil || prior.Sum != sum || string(prior.Reply) != "\x01\x02" || prior.Code != 5 || prior.Message != "gone" {
		t.Fatalf("the other replica after keep = %q, %+v, %v", again, prior, err)
	}
}

func TestADroppedOrExpiredKeyCanBeClaimedAgain(t *testing.T) {
	s, now := replayStores(t, 1)
	r := s[0]
	token, _, _ := r.Claim(ctx, "k", [32]byte{})
	r.Drop(ctx, "k", token)
	if token, prior, err := r.Claim(ctx, "k", [32]byte{}); token == "" || prior != nil || err != nil {
		t.Fatalf("claim after drop = %q, %v, %v", token, prior, err)
	}
	*now = now.Add(2 * time.Minute)
	token, prior, err := r.Claim(ctx, "k", [32]byte{3})
	if token == "" || prior != nil || err != nil {
		t.Fatalf("an abandoned claim past its TTL still held the key: %q, %v, %v", token, prior, err)
	}
	r.Keep(ctx, "k", token, kernel.Replayed{Sum: [32]byte{3}, Reply: []byte("r")})
	*now = now.Add(30 * time.Second)
	if _, prior, _ := r.Claim(ctx, "k", [32]byte{3}); prior == nil {
		t.Fatal("a kept outcome was forgotten before its TTL")
	}
	*now = now.Add(31 * time.Second)
	if token, prior, err := r.Claim(ctx, "k", [32]byte{3}); token == "" || prior != nil || err != nil {
		t.Fatalf("a kept outcome outlived its TTL: %q, %v, %v", token, prior, err)
	}
}

func TestAReplicaPastItsTTLCanNeitherKeepNorDropTheNextOwnersClaim(t *testing.T) {
	s, now := replayStores(t, 2)
	slow, fresh := s[0], s[1]
	late, _, _ := slow.Claim(ctx, "k", [32]byte{1})
	*now = now.Add(30 * time.Second)
	fresh.Claim(ctx, "swept", [32]byte{})
	*now = now.Add(31 * time.Second)
	next, _, err := fresh.Claim(ctx, "k", [32]byte{2})
	if next == "" || next == late || err != nil {
		t.Fatalf("a claim after the first expired = %q, %v", next, err)
	}
	slow.Keep(ctx, "k", late, kernel.Replayed{Sum: [32]byte{1}, Reply: []byte("late")})
	slow.Drop(ctx, "k", late)
	if _, _, err := fresh.Claim(ctx, "k", [32]byte{2}); !errors.Is(err, kernel.ErrInFlight) {
		t.Fatalf("the next owner's claim after the late keep and drop = %v, want ErrInFlight", err)
	}
	fresh.Keep(ctx, "k", next, kernel.Replayed{Sum: [32]byte{2}, Reply: []byte("next")})
	if _, prior, err := slow.Claim(ctx, "k", [32]byte{2}); err != nil || prior == nil || string(prior.Reply) != "next" {
		t.Fatalf("the kept outcome = %+v, %v", prior, err)
	}
}

func TestExpiredRowsAreSweptAway(t *testing.T) {
	s, now := replayStores(t, 1)
	r := s[0]
	for _, k := range []string{"a", "b", "c"} {
		token, _, _ := r.Claim(ctx, k, [32]byte{})
		r.Keep(ctx, k, token, kernel.Replayed{})
	}
	*now = now.Add(3 * time.Minute)
	r.Claim(ctx, "fresh", [32]byte{})
	var rows []struct {
		Key string `db:"key"`
	}
	if err := r.s.QueryRowsCtx(ctx, &rows, `SELECT key FROM idempotency`); err != nil || len(rows) != 1 || rows[0].Key != "fresh" {
		t.Fatalf("rows after the sweep = %+v, %v", rows, err)
	}
}

func TestAReplayStoreWithoutAPositiveTTLIsRefused(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := NewReplayStore(open(t, pool), "idempotency", ttl); err == nil || !strings.Contains(err.Error(), "ttl") {
			t.Errorf("ttl %v = %v", ttl, err)
		}
	}
}

func TestATableNameThatIsNotAnIdentifierIsRefused(t *testing.T) {
	if _, err := NewReplayStore(open(t, pool), "x; DROP TABLE links", time.Minute); err == nil || !strings.Contains(err.Error(), "not a table name") {
		t.Fatalf("err = %v", err)
	}
}
