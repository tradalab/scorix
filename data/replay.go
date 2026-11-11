package data

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/tradalab/scorix/kernel"
)

var tableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

type sqlReplay struct {
	s     Session
	table string
	ttl   time.Duration
	now   func() time.Time
	mu    sync.Mutex
	sweep time.Time
}

type replayRow struct {
	Sum     string `db:"sum"`
	Done    bool   `db:"done"`
	Reply   []byte `db:"reply"`
	Code    int32  `db:"code"`
	Message string `db:"message"`
}

// NewReplayStore keeps idempotency outcomes in table, so a retry reaching any replica gets the first
// outcome. The table has key TEXT PRIMARY KEY, claim TEXT, sum TEXT, done BOOLEAN, reply BYTEA, code
// INTEGER, message TEXT and expires_at BIGINT (Unix nanoseconds); rows live for ttl.
func NewReplayStore(s Session, table string, ttl time.Duration) (kernel.ReplayStore, error) {
	switch {
	case !tableRe.MatchString(table):
		return nil, fmt.Errorf("data: %q is not a table name", table)
	case ttl <= 0:
		return nil, fmt.Errorf("data: replay ttl %v is not positive", ttl)
	}
	return &sqlReplay{s: s, table: table, ttl: ttl, now: time.Now}, nil
}

func (r *sqlReplay) Claim(ctx context.Context, key string, sum [32]byte) (string, *kernel.Replayed, error) {
	now := r.now()
	if err := r.sweepOnce(ctx, now); err != nil {
		return "", nil, err
	}
	token := rand.Text()
	res, err := r.s.ExecCtx(ctx, `INSERT INTO `+r.table+` (key, claim, sum, done, reply, code, message, expires_at) VALUES ($1, $2, $3, false, NULL, 0, '', $4)
ON CONFLICT (key) DO UPDATE SET claim = excluded.claim, sum = excluded.sum, done = false, reply = NULL, code = 0, message = '', expires_at = excluded.expires_at
WHERE `+r.table+`.expires_at < $5`, key, token, hex.EncodeToString(sum[:]), now.Add(r.ttl).UnixNano(), now.UnixNano())
	if err != nil {
		return "", nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return "", nil, err
	} else if n == 1 {
		return token, nil, nil
	}
	var row replayRow
	err = r.s.QueryRowCtx(ctx, &row, `SELECT sum, done, reply, code, message FROM `+r.table+` WHERE key = $1`, key)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", nil, kernel.ErrInFlight
	case err != nil:
		return "", nil, err
	case !row.Done:
		return "", nil, kernel.ErrInFlight
	}
	out := &kernel.Replayed{Reply: row.Reply, Code: row.Code, Message: row.Message}
	if b, err := hex.DecodeString(row.Sum); err == nil {
		copy(out.Sum[:], b)
	}
	return "", out, nil
}

func (r *sqlReplay) sweepOnce(ctx context.Context, now time.Time) error {
	r.mu.Lock()
	due := now.After(r.sweep)
	if due {
		r.sweep = now.Add(r.ttl)
	}
	r.mu.Unlock()
	if !due {
		return nil
	}
	_, err := r.s.ExecCtx(ctx, `DELETE FROM `+r.table+` WHERE expires_at < $1`, now.UnixNano())
	return err
}

func (r *sqlReplay) Keep(ctx context.Context, key, token string, out kernel.Replayed) error {
	_, err := r.s.ExecCtx(ctx, `UPDATE `+r.table+` SET sum = $3, done = true, reply = $4, code = $5, message = $6, expires_at = $7 WHERE key = $1 AND claim = $2`,
		key, token, hex.EncodeToString(out.Sum[:]), out.Reply, out.Code, out.Message, r.now().Add(r.ttl).UnixNano())
	return err
}

func (r *sqlReplay) Drop(ctx context.Context, key, token string) error {
	_, err := r.s.ExecCtx(ctx, `DELETE FROM `+r.table+` WHERE key = $1 AND claim = $2`, key, token)
	return err
}
