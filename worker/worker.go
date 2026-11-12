// Package worker runs durable jobs out of one SQL table. Enqueued through the transaction of
// a write, a job exists exactly when that write committed (an outbox); a cron slot is a job
// keyed by its time, so replicas keeping one schedule run each slot once.
package worker

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/tradalab/scorix/data"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
)

var tableRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

// Options are optional except Table. Lease bounds one run: a handler must finish within it,
// since past it plus the next attempt's Backoff another worker takes the job over. Leases use
// each process's clock, so replicas must agree to within that Backoff, 1s on a first attempt.
type Options struct {
	Table string
	// Concurrency is how many jobs run at once; zero means 4.
	Concurrency int
	// Poll is how often due jobs are looked for; zero means 1s.
	Poll time.Duration
	// Lease is how long one run may take; zero means 5m.
	Lease time.Duration
	// MaxAttempts ends a job as failed after that many runs, counting ones whose lease
	// expired; zero means 10.
	MaxAttempts int
	// Retain is how long finished jobs are kept; zero means 7 days.
	Retain time.Duration
	// Location is the time zone cron schedules read in; nil means UTC.
	Location *time.Location
	// Observer sees each run as a call of transport "worker" whose procedure is the kind.
	Observer kernel.Observer
}

type handler struct {
	decode func(payload string) (proto.Message, error)
	run    func(ctx context.Context, msg proto.Message) error
}

type cronEntry struct {
	name    string
	sched   Schedule
	planned time.Time
}

type Worker struct {
	db       *data.DB
	opt      Options
	now      func() time.Time
	mu       sync.Mutex
	handlers map[string]handler
	crons    []*cronEntry
	swept    time.Time
}

// New checks opt; call EnsureTable, or create the same table in a migration, before Run.
func New(db *data.DB, opt Options) (*Worker, error) {
	if !tableRe.MatchString(opt.Table) {
		return nil, fmt.Errorf("worker: Options.Table %q is not a table name", opt.Table)
	}
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.Poll <= 0 {
		opt.Poll = time.Second
	}
	if opt.Lease <= 0 {
		opt.Lease = 5 * time.Minute
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 10
	}
	if opt.Retain <= 0 {
		opt.Retain = 7 * 24 * time.Hour
	}
	if opt.Location == nil {
		opt.Location = time.UTC
	}
	return &Worker{db: db, opt: opt, now: time.Now, handlers: map[string]handler{}}, nil
}

// DDL is the table and index New's Table needs, valid on Postgres and SQLite.
func (w *Worker) DDL() []string {
	t := w.opt.Table
	index := t[strings.LastIndexByte(t, '.')+1:] + "_due"
	return []string{
		`CREATE TABLE IF NOT EXISTS ` + t + ` (
  id TEXT PRIMARY KEY,
  kind TEXT NOT NULL,
  dedup TEXT UNIQUE,
  payload TEXT NOT NULL,
  run_at BIGINT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  locked_until BIGINT NOT NULL DEFAULT 0,
  done_at BIGINT,
  failed BOOLEAN NOT NULL DEFAULT FALSE,
  last_error TEXT NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS ` + index + ` ON ` + t + ` (run_at) WHERE done_at IS NULL`,
	}
}

// EnsureTable runs DDL.
func (w *Worker) EnsureTable(ctx context.Context) error {
	for _, q := range w.DDL() {
		if _, err := w.db.ExecCtx(ctx, q); err != nil {
			return fmt.Errorf("worker: %s: %w", w.opt.Table, err)
		}
	}
	return nil
}

func message[T any]() proto.Message {
	m, ok := any(new(T)).(proto.Message)
	if !ok {
		panic(fmt.Sprintf("worker: %T is not a proto message", new(T)))
	}
	return m
}

func (w *Worker) register(kind string, h handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, dup := w.handlers[kind]; dup {
		panic("worker: kind " + kind + " registered twice")
	}
	w.handlers[kind] = h
}

// Handle runs fn for every job of kind. It panics when kind is registered twice.
func Handle[T any](w *Worker, kind string, fn func(ctx context.Context, job *T) error) {
	sample := message[T]()
	w.register(kind, handler{
		decode: func(payload string) (proto.Message, error) {
			m := sample.ProtoReflect().New().Interface()
			return m, protojson.Unmarshal([]byte(payload), m)
		},
		run: func(ctx context.Context, m proto.Message) error { return fn(ctx, any(m).(*T)) },
	})
}

// Cron runs fn at every minute sched names, in Options.Location, once across all workers on
// the table; fn gets the slot's time. A slot that passed while no worker ran is skipped.
func (w *Worker) Cron(name string, sched Schedule, fn func(ctx context.Context, at time.Time) error) {
	Handle(w, "cron:"+name, func(ctx context.Context, at *timestamppb.Timestamp) error {
		return fn(ctx, at.AsTime().In(w.opt.Location))
	})
	w.mu.Lock()
	w.crons = append(w.crons, &cronEntry{name: name, sched: sched})
	w.mu.Unlock()
}

type enqueue struct {
	at  time.Time
	key string
}

type EnqueueOption func(*enqueue)

// At runs the job no earlier than t.
func At(t time.Time) EnqueueOption { return func(e *enqueue) { e.at = t } }

// Key makes the job unique: enqueueing a second job with the same key does nothing, even
// after the first finished, until Retain removes it.
func Key(k string) EnqueueOption { return func(e *enqueue) { e.key = k } }

// Enqueue adds a job of kind through s, which may be the transaction of the write the job
// follows from. It reports false when Key named a job that already exists.
func (w *Worker) Enqueue(ctx context.Context, s data.Session, kind string, payload proto.Message, opts ...EnqueueOption) (bool, error) {
	return insert(ctx, s, w.opt.Table, w.now(), kind, payload, opts)
}

// Enqueue adds a job of kind to table through s as Worker.Enqueue does, for a service that
// only enqueues and so holds no Worker; a job without At is due now. It fails on a bad table name.
func Enqueue(ctx context.Context, s data.Session, table, kind string, payload proto.Message, opts ...EnqueueOption) (bool, error) {
	if !tableRe.MatchString(table) {
		return false, fmt.Errorf("worker: %q is not a table name", table)
	}
	return insert(ctx, s, table, time.Now(), kind, payload, opts)
}

func insert(ctx context.Context, s data.Session, table string, now time.Time, kind string, payload proto.Message, opts []EnqueueOption) (bool, error) {
	e := enqueue{at: now}
	for _, o := range opts {
		o(&e)
	}
	body, err := protojson.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("worker: %s payload: %w", kind, err)
	}
	var key any
	if e.key != "" {
		key = e.key
	}
	res, err := s.ExecCtx(ctx, `INSERT INTO `+table+` (id, kind, dedup, payload, run_at) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (dedup) DO NOTHING`,
		uuid.NewString(), kind, key, string(body), e.at.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("worker: enqueue %s: %w", kind, err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Permanent marks err as one a retry cannot fix, so the job ends as failed at once; nil
// stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// Backoff is the wait before attempt n+1: 1s doubling per attempt, at most an hour, and
// zero for n <= 0.
func Backoff(n int) time.Duration {
	switch {
	case n <= 0:
		return 0
	case n > 12:
		return time.Hour
	}
	return min(time.Second<<(n-1), time.Hour)
}

type claimed struct {
	ID       string `db:"id"`
	Kind     string `db:"kind"`
	Payload  string `db:"payload"`
	Attempts int    `db:"attempts"`
	Locked   int64  `db:"locked_until"`
}

func (w *Worker) claim(ctx context.Context, limit int) ([]claimed, error) {
	now := w.now().UnixMilli()
	args := []any{now, limit}
	var kinds []string
	w.mu.Lock()
	for kind := range w.handlers {
		args = append(args, kind)
		kinds = append(kinds, fmt.Sprintf("$%d", len(args)))
	}
	w.mu.Unlock()
	if len(kinds) == 0 {
		return nil, nil
	}
	var due []claimed
	if err := w.db.QueryRowsCtx(ctx, &due, `SELECT id, kind, payload, attempts, locked_until FROM `+w.opt.Table+
		` WHERE done_at IS NULL AND run_at <= $1 AND locked_until <= $1 AND kind IN (`+strings.Join(kinds, ", ")+`) ORDER BY run_at LIMIT $2`, args...); err != nil {
		return nil, err
	}
	lease := now + w.opt.Lease.Milliseconds()
	var mine []claimed
	for _, j := range due {
		if j.Attempts >= w.opt.MaxAttempts {
			if err := w.expire(ctx, j, now); err != nil {
				return mine, err
			}
			continue
		}
		ok, err := w.take(ctx, &j, lease)
		if err != nil {
			return mine, err
		}
		if ok {
			mine = append(mine, j)
		}
	}
	return mine, nil
}

func (w *Worker) expire(ctx context.Context, j claimed, now int64) error {
	msg := fmt.Sprintf("worker: lease expired after %d attempts", j.Attempts)
	_, err := w.db.ExecCtx(ctx, `UPDATE `+w.opt.Table+` SET done_at = $1, failed = TRUE, locked_until = 0, last_error = $2 WHERE id = $3 AND locked_until = $4 AND done_at IS NULL`,
		now, msg, j.ID, j.Locked)
	if err == nil {
		observe.Log(ctx).Warn("worker: job failed", "kind", j.Kind, "id", j.ID, "attempt", j.Attempts, "error", msg)
	}
	return err
}

func (w *Worker) take(ctx context.Context, j *claimed, lease int64) (bool, error) {
	retry := lease + Backoff(j.Attempts+1).Milliseconds()
	res, err := w.db.ExecCtx(ctx, `UPDATE `+w.opt.Table+` SET locked_until = $1, attempts = attempts + 1, run_at = $2 WHERE id = $3 AND locked_until = $4 AND done_at IS NULL`,
		lease, retry, j.ID, j.Locked)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return false, nil
	}
	j.Attempts++
	j.Locked = lease
	return true, nil
}

func (w *Worker) run(ctx context.Context, j claimed) (err error) {
	ctx, done := kernel.Observe(ctx, w.opt.Observer, "worker", kernel.Call{Procedure: j.Kind})
	defer func() { done(err) }()
	w.mu.Lock()
	h := w.handlers[j.Kind]
	w.mu.Unlock()
	msg, err := h.decode(j.Payload)
	if err != nil {
		return Permanent(fmt.Errorf("worker: %s payload: %w", j.Kind, err))
	}
	ctx, cancel := context.WithTimeout(ctx, w.opt.Lease)
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("worker: %s panicked: %v\n%s", j.Kind, p, debug.Stack())
		}
	}()
	return h.run(ctx, msg)
}

func (w *Worker) settle(ctx context.Context, j claimed, runErr error) error {
	now := w.now()
	q, args := `UPDATE `+w.opt.Table+` SET done_at = $1, locked_until = 0, last_error = '' WHERE id = $2 AND locked_until = $3`, []any{now.UnixMilli(), j.ID, j.Locked}
	if runErr != nil {
		msg := runErr.Error()
		if _, perm := errors.AsType[permanent](runErr); perm || j.Attempts >= w.opt.MaxAttempts {
			q, args = `UPDATE `+w.opt.Table+` SET done_at = $1, failed = TRUE, locked_until = 0, last_error = $2 WHERE id = $3 AND locked_until = $4`, []any{now.UnixMilli(), msg, j.ID, j.Locked}
		} else {
			q, args = `UPDATE `+w.opt.Table+` SET run_at = $1, locked_until = 0, last_error = $2 WHERE id = $3 AND locked_until = $4`, []any{now.Add(Backoff(j.Attempts)).UnixMilli(), msg, j.ID, j.Locked}
		}
		observe.Log(ctx).Warn("worker: job failed", "kind", j.Kind, "id", j.ID, "attempt", j.Attempts, "error", msg)
	}
	res, err := w.db.ExecCtx(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		observe.Log(ctx).Warn("worker: a job outlived its lease and was taken over", "kind", j.Kind, "id", j.ID)
	}
	return nil
}

func (w *Worker) plan(ctx context.Context) error {
	now := w.now().In(w.opt.Location)
	w.mu.Lock()
	crons := append([]*cronEntry(nil), w.crons...)
	w.mu.Unlock()
	for _, c := range crons {
		if c.planned.IsZero() {
			c.planned = now.Add(-time.Minute)
		}
		for next := c.sched.Next(c.planned); !next.IsZero() && !next.After(now); next = c.sched.Next(next) {
			if _, err := w.Enqueue(ctx, w.db, "cron:"+c.name, timestamppb.New(next), At(next), Key(fmt.Sprintf("cron:%s:%d", c.name, next.Unix()))); err != nil {
				return err
			}
			c.planned = next
		}
	}
	return nil
}

func (w *Worker) sweep(ctx context.Context) error {
	now := w.now()
	if !w.swept.IsZero() && now.Sub(w.swept) < time.Hour {
		return nil
	}
	w.swept = now
	_, err := w.db.ExecCtx(ctx, `DELETE FROM `+w.opt.Table+` WHERE done_at < $1`, now.Add(-w.opt.Retain).UnixMilli())
	return err
}

// Tick plans due cron slots, sweeps old jobs, and runs what is due now, up to Concurrency at
// once; it returns when those runs have settled. Run calls it every Poll.
func (w *Worker) Tick(ctx context.Context) error {
	if err := w.plan(ctx); err != nil {
		return fmt.Errorf("worker: cron: %w", err)
	}
	if err := w.sweep(ctx); err != nil {
		return fmt.Errorf("worker: sweep: %w", err)
	}
	jobs, err := w.claim(ctx, w.opt.Concurrency)
	if err != nil {
		return fmt.Errorf("worker: claim: %w", err)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(jobs))
	for i, j := range jobs {
		wg.Go(func() {
			runCtx := context.WithoutCancel(ctx)
			errs[i] = w.settle(runCtx, j, w.run(runCtx, j))
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Run ticks every Poll until ctx ends, logging what a tick could not do; runs in progress
// finish, within their Lease, before it returns.
func (w *Worker) Run(ctx context.Context) error {
	t := time.NewTicker(w.opt.Poll)
	defer t.Stop()
	for {
		if err := w.Tick(ctx); err != nil && ctx.Err() == nil {
			observe.Log(ctx).Error("worker: tick", "error", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
