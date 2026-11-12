package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/data"
	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
)

var bg = context.Background()

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func open(t *testing.T) *data.DB {
	t.Helper()
	db, err := data.Open(bg, "sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "_")+"?mode=memory&cache=shared", data.Options{MaxOpen: 1, MaxIdle: 1, MaxLifetime: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func worker(t *testing.T, db *data.DB, c *clock, opt Options) *Worker {
	t.Helper()
	if opt.Table == "" {
		opt.Table = "jobs"
	}
	w, err := New(db, opt)
	if err != nil {
		t.Fatal(err)
	}
	w.now = c.now
	if err := w.EnsureTable(bg); err != nil {
		t.Fatal(err)
	}
	return w
}

type row struct {
	Kind      string `db:"kind"`
	Attempts  int    `db:"attempts"`
	Done      *int64 `db:"done_at"`
	Failed    bool   `db:"failed"`
	LastError string `db:"last_error"`
	RunAt     int64  `db:"run_at"`
}

func rows(t *testing.T, db *data.DB) []row {
	t.Helper()
	var out []row
	if err := db.QueryRowsCtx(bg, &out, `SELECT kind, attempts, done_at, failed, last_error, run_at FROM jobs ORDER BY run_at, kind`); err != nil {
		t.Fatal(err)
	}
	return out
}

func start() *clock { return &clock{t: time.Date(2026, 11, 11, 10, 0, 30, 0, time.UTC)} }

func TestAJobRunsOnceWithItsPayload(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	var got []string
	Handle(w, "greet", func(_ context.Context, in *wrapperspb.StringValue) error {
		got = append(got, in.GetValue())
		return nil
	})
	if ok, err := w.Enqueue(bg, db, "greet", wrapperspb.String("ana")); !ok || err != nil {
		t.Fatalf("Enqueue = %v %v", ok, err)
	}
	for range 3 {
		if err := w.Tick(bg); err != nil {
			t.Fatal(err)
		}
	}
	r := rows(t, db)
	if len(got) != 1 || got[0] != "ana" || len(r) != 1 || r[0].Done == nil || r[0].Failed || r[0].Attempts != 1 {
		t.Fatalf("ran %v, row %+v", got, r)
	}
}

func TestAJobEnqueuedInATransactionExistsOnlyIfItCommits(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	boom := errors.New("the write failed")
	db.TransactCtx(bg, func(ctx context.Context, s data.Session) error {
		w.Enqueue(ctx, s, "greet", wrapperspb.String("lost"))
		return boom
	})
	if err := db.TransactCtx(bg, func(ctx context.Context, s data.Session) error {
		_, err := w.Enqueue(ctx, s, "greet", wrapperspb.String("kept"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if r := rows(t, db); len(r) != 1 {
		t.Fatalf("rows = %+v, want only the committed job", r)
	}
}

func TestAKeyedJobIsEnqueuedOnceEvenAfterItRan(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	var n atomic.Int32
	Handle(w, "greet", func(context.Context, *wrapperspb.StringValue) error { n.Add(1); return nil })
	if ok, _ := w.Enqueue(bg, db, "greet", wrapperspb.String("a"), Key("k1")); !ok {
		t.Fatal("first keyed enqueue refused")
	}
	if ok, _ := w.Enqueue(bg, db, "greet", wrapperspb.String("a"), Key("k1")); ok {
		t.Fatal("a second job with the same key was added")
	}
	w.Tick(bg)
	if ok, _ := w.Enqueue(bg, db, "greet", wrapperspb.String("a"), Key("k1")); ok {
		t.Fatal("a key was reused after its job ran")
	}
	w.Enqueue(bg, db, "greet", wrapperspb.String("a"))
	w.Enqueue(bg, db, "greet", wrapperspb.String("a"))
	w.Tick(bg)
	if n.Load() != 3 {
		t.Fatalf("ran %d jobs, want the keyed one once and two unkeyed", n.Load())
	}
}

func TestAJobWaitsForItsTime(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	n := 0
	Handle(w, "later", func(context.Context, *wrapperspb.StringValue) error { n++; return nil })
	w.Enqueue(bg, db, "later", wrapperspb.String(""), At(c.now().Add(time.Minute)))
	w.Tick(bg)
	if n != 0 {
		t.Fatal("ran before its time")
	}
	c.add(time.Minute)
	w.Tick(bg)
	if n != 1 {
		t.Fatal("did not run at its time")
	}
}

func TestAFailedJobRetriesWithBackoffUntilItsAttemptsRunOut(t *testing.T) {
	db, c := open(t), start()
	var logs bytes.Buffer
	ctx := observe.WithLogger(bg, slog.New(slog.NewTextHandler(&logs, nil)))
	w := worker(t, db, c, Options{MaxAttempts: 3})
	n := 0
	Handle(w, "flaky", func(context.Context, *wrapperspb.StringValue) error { n++; return errors.New("not yet") })
	w.Enqueue(bg, db, "flaky", wrapperspb.String(""))
	w.Tick(ctx)
	r := rows(t, db)[0]
	if n != 1 || r.Done != nil || r.LastError != "not yet" || r.RunAt != c.now().Add(time.Second).UnixMilli() {
		t.Fatalf("after one failure: ran %d, row %+v", n, r)
	}
	w.Tick(ctx)
	if n != 1 {
		t.Fatal("retried before its backoff")
	}
	c.add(time.Second)
	w.Tick(ctx)
	if r := rows(t, db)[0]; n != 2 || r.RunAt != c.now().Add(2*time.Second).UnixMilli() {
		t.Fatalf("second backoff: ran %d, row %+v", n, r)
	}
	c.add(2 * time.Second)
	w.Tick(ctx)
	c.add(time.Hour)
	w.Tick(ctx)
	if r := rows(t, db)[0]; n != 3 || r.Done == nil || !r.Failed || r.Attempts != 3 {
		t.Fatalf("after its attempts ran out: ran %d, row %+v", n, r)
	}
	if !strings.Contains(logs.String(), "kind=flaky") {
		t.Fatalf("failures were not logged: %s", logs.String())
	}
}

func TestAPermanentErrorABadPayloadOrAPanicEndTheJob(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	Handle(w, "perm", func(context.Context, *wrapperspb.StringValue) error { return Permanent(errors.New("no such user")) })
	Handle(w, "panics", func(context.Context, *wrapperspb.StringValue) error { panic("oops") })
	w.Enqueue(bg, db, "perm", wrapperspb.String(""))
	w.Enqueue(bg, db, "panics", wrapperspb.String(""))
	db.ExecCtx(bg, `INSERT INTO jobs (id, kind, payload, run_at) VALUES ('x', 'perm', '{not json', 0)`)
	w.Tick(bg)
	for _, r := range rows(t, db) {
		switch {
		case r.Kind == "panics":
			if r.Done != nil || !strings.Contains(r.LastError, "panicked: oops") {
				t.Errorf("a panic = %+v, want a retry that names it", r)
			}
		case r.Done == nil || !r.Failed:
			t.Errorf("%s = %+v, want ended as failed", r.Kind, r)
		}
	}
}

func TestAKindNoHandlerKnowsIsLeftForAReplicaThatHasOne(t *testing.T) {
	db, c := open(t), start()
	old := worker(t, db, c, Options{})
	newer := worker(t, db, c, Options{})
	Handle(old, "known", func(context.Context, *wrapperspb.StringValue) error { return nil })
	ran := 0
	Handle(newer, "from-a-newer-build", func(context.Context, *wrapperspb.StringValue) error { ran++; return nil })
	old.Enqueue(bg, db, "from-a-newer-build", wrapperspb.String(""))
	for _, w := range []*Worker{old, worker(t, db, c, Options{})} {
		if err := w.Tick(bg); err != nil {
			t.Fatal(err)
		}
	}
	if r := rows(t, db)[0]; r.Done != nil || r.Attempts != 0 || r.LastError != "" {
		t.Fatalf("a replica without the handler touched the job: %+v", r)
	}
	newer.Tick(bg)
	if r := rows(t, db)[0]; ran != 1 || r.Done == nil {
		t.Fatalf("the replica with the handler ran %d, row %+v", ran, r)
	}
}

func TestACrashedRunWaitsItsBackoffAndEndsAsFailedOnceItsAttemptsRunOut(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{Lease: time.Minute, MaxAttempts: 2})
	ran := 0
	Handle(w, "poison", func(context.Context, *wrapperspb.StringValue) error { ran++; return nil })
	w.Enqueue(bg, db, "poison", wrapperspb.String(""))
	for attempt := 1; attempt <= 2; attempt++ {
		if got, err := w.claim(bg, 1); err != nil || len(got) != 1 || got[0].Attempts != attempt {
			t.Fatalf("attempt %d: claim = %+v %v", attempt, got, err)
		}
		c.add(time.Minute)
		if got, _ := w.claim(bg, 1); len(got) != 0 {
			t.Fatalf("attempt %d: a crashed run was retried before Backoff(%d)", attempt, attempt)
		}
		c.add(Backoff(attempt))
	}
	if err := w.Tick(bg); err != nil {
		t.Fatal(err)
	}
	if r := rows(t, db)[0]; ran != 0 || r.Done == nil || !r.Failed || r.Attempts != 2 || r.LastError != "worker: lease expired after 2 attempts" {
		t.Fatalf("a job that crashed its process twice: ran %d, row %+v", ran, r)
	}
}

func TestAJobThatOutlivesItsLeaseIsTakenOverAndTheLateResultDropped(t *testing.T) {
	db, c := open(t), start()
	var logs bytes.Buffer
	ctx := observe.WithLogger(bg, slog.New(slog.NewTextHandler(&logs, nil)))
	a := worker(t, db, c, Options{Lease: time.Minute})
	b := worker(t, db, c, Options{Lease: time.Minute})
	ran := 0
	for _, w := range []*Worker{a, b} {
		Handle(w, "slow", func(context.Context, *wrapperspb.StringValue) error { ran++; return nil })
	}
	a.Enqueue(bg, db, "slow", wrapperspb.String(""))
	held, err := a.claim(bg, 1)
	if err != nil || len(held) != 1 {
		t.Fatalf("claim = %v %v", held, err)
	}
	if again, _ := b.claim(bg, 1); len(again) != 0 {
		t.Fatal("a leased job was claimed twice")
	}
	c.add(time.Minute + time.Second)
	b.Tick(ctx)
	a.settle(ctx, held[0], nil)
	if r := rows(t, db)[0]; ran != 1 || r.Attempts != 2 || r.Done == nil {
		t.Fatalf("ran %d, row %+v", ran, r)
	}
	if !strings.Contains(logs.String(), "outlived its lease") {
		t.Fatalf("the late result was not reported: %s", logs.String())
	}
}

func TestEachCronSlotRunsOnceAcrossWorkers(t *testing.T) {
	db, c := open(t), start()
	sched, _ := ParseSchedule("*/2 * * * *")
	var mu sync.Mutex
	var slots []time.Time
	var ws []*Worker
	for range 2 {
		w := worker(t, db, c, Options{})
		w.Cron("tidy", sched, func(_ context.Context, at time.Time) error {
			mu.Lock()
			slots = append(slots, at)
			mu.Unlock()
			return nil
		})
		ws = append(ws, w)
	}
	for range 5 {
		for _, w := range ws {
			if err := w.Tick(bg); err != nil {
				t.Fatal(err)
			}
		}
		c.add(time.Minute)
	}
	want := []time.Time{time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 11, 10, 2, 0, 0, time.UTC), time.Date(2026, 11, 11, 10, 4, 0, 0, time.UTC)}
	if len(slots) != len(want) {
		t.Fatalf("slots = %v, want %v", slots, want)
	}
	for i := range want {
		if !slots[i].Equal(want[i]) {
			t.Fatalf("slots = %v, want %v", slots, want)
		}
	}
}

func TestCronReadsItsZoneAndALiveWorkerCatchesUpOnSlotsItDidNotTick(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{Location: time.FixedZone("ICT", 7*3600)})
	sched, _ := ParseSchedule("0 17 * * *")
	var mu sync.Mutex
	var got []time.Time
	w.Cron("daily", sched, func(_ context.Context, at time.Time) error {
		mu.Lock()
		got = append(got, at)
		mu.Unlock()
		return nil
	})
	w.Tick(bg)
	if len(got) != 1 || got[0].Hour() != 17 || got[0].Location().String() != "ICT" {
		t.Fatalf("10:00 UTC is 17:00 ICT, so the slot that just came is due: %v", got)
	}
	c.add(3 * 24 * time.Hour)
	w.Tick(bg)
	w.Tick(bg)
	if len(got) != 4 {
		t.Fatalf("after three days without a tick the worker ran %d slots, want each day once", len(got))
	}
}

func TestAWorkerThatStartsLateSkipsTheSlotsThatPassedBeforeIt(t *testing.T) {
	db, c := open(t), start()
	c.add(3*24*time.Hour + 5*time.Minute)
	w := worker(t, db, c, Options{})
	sched, _ := ParseSchedule("0 10 * * *")
	n := 0
	w.Cron("daily", sched, func(context.Context, time.Time) error { n++; return nil })
	w.Tick(bg)
	if n != 0 {
		t.Fatalf("a worker started at 10:05 ran %d slots that passed before it", n)
	}
	c.add(24 * time.Hour)
	w.Tick(bg)
	w.Tick(bg)
	if n != 1 {
		t.Fatalf("the next slot ran %d times", n)
	}
}

func TestFinishedJobsAreSweptAfterRetainAtMostHourly(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{Retain: 24 * time.Hour})
	Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { return nil })
	w.Enqueue(bg, db, "x", wrapperspb.String(""))
	w.Tick(bg)
	w.Enqueue(bg, db, "x", wrapperspb.String(""), At(c.now().Add(48*time.Hour)))
	c.add(25 * time.Hour)
	w.Tick(bg)
	if r := rows(t, db); len(r) != 1 || r[0].Done != nil {
		t.Fatalf("after Retain: %+v, want only the pending job", r)
	}
	w.Enqueue(bg, db, "x", wrapperspb.String(""))
	w.Tick(bg)
	db.ExecCtx(bg, `UPDATE jobs SET done_at = 0 WHERE done_at IS NOT NULL`)
	c.add(30 * time.Minute)
	w.Tick(bg)
	if r := rows(t, db); len(r) != 2 {
		t.Fatalf("a second sweep within the hour removed %+v", r)
	}
}

type watch struct {
	mu    sync.Mutex
	calls []string
}

func (o *watch) Start(ctx context.Context, transport string, c kernel.Call) (context.Context, func(error)) {
	return ctx, func(err error) {
		o.mu.Lock()
		o.calls = append(o.calls, transport+" "+c.Procedure+" "+map[bool]string{true: "ok", false: "err"}[err == nil])
		o.mu.Unlock()
	}
}

func TestEachRunIsObserved(t *testing.T) {
	db, c := open(t), start()
	o := &watch{}
	w := worker(t, db, c, Options{Observer: o})
	Handle(w, "ok", func(context.Context, *wrapperspb.StringValue) error { return nil })
	Handle(w, "bad", func(context.Context, *wrapperspb.StringValue) error { return errors.New("x") })
	w.Enqueue(bg, db, "ok", wrapperspb.String(""))
	w.Enqueue(bg, db, "bad", wrapperspb.String(""))
	w.Tick(bg)
	got := strings.Join(o.calls, ",")
	if !strings.Contains(got, "worker ok ok") || !strings.Contains(got, "worker bad err") {
		t.Fatalf("observed %s", got)
	}
}

func TestConcurrencyBoundsOneTick(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{Concurrency: 2})
	n := 0
	var mu sync.Mutex
	Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { mu.Lock(); n++; mu.Unlock(); return nil })
	for range 5 {
		w.Enqueue(bg, db, "x", wrapperspb.String(""))
	}
	w.Tick(bg)
	if n != 2 {
		t.Fatalf("one tick ran %d jobs, want 2", n)
	}
}

func TestRunTicksUntilItsContextEnds(t *testing.T) {
	db := open(t)
	w, _ := New(db, Options{Table: "jobs", Poll: 10 * time.Millisecond})
	w.EnsureTable(bg)
	ran := make(chan struct{}, 1)
	Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { ran <- struct{}{}; return nil })
	ctx, cancel := context.WithCancel(bg)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	w.Enqueue(bg, db, "x", wrapperspb.String(""))
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run never ran the job")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMisuseIsRefused(t *testing.T) {
	if _, err := New(nil, Options{Table: "jobs; DROP TABLE x"}); err == nil {
		t.Error("a table name with SQL in it")
	}
	if _, err := New(nil, Options{Table: "shorten.jobs"}); err != nil {
		t.Errorf("a schema-qualified table: %v", err)
	}
	w, _ := New(nil, Options{Table: "shorten.jobs"})
	if ddl := w.DDL(); !strings.Contains(ddl[1], "jobs_due ON shorten.jobs") {
		t.Errorf("index DDL = %s", ddl[1])
	}
	Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { return nil })
	for name, fn := range map[string]func(){
		"twice":     func() { Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { return nil }) },
		"not proto": func() { Handle(w, "y", func(context.Context, *struct{}) error { return nil }) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", name)
				}
			}()
			fn()
		}()
	}
}

func TestBackoffDoublesToAnHour(t *testing.T) {
	for n, want := range map[int]time.Duration{-3: 0, 0: 0, 1: time.Second, 2: 2 * time.Second, 5: 16 * time.Second, 12: 2048 * time.Second, 13: time.Hour, 40: time.Hour} {
		if got := Backoff(n); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, want)
		}
	}
}

func TestPermanentOfNilIsNil(t *testing.T) {
	if err := Permanent(nil); err != nil {
		t.Fatalf("Permanent(nil) = %#v", err)
	}
}

func TestTwoWorkersHoldingTheSameSnapshotTakeTheJobOnce(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	w.Enqueue(bg, db, "x", wrapperspb.String(""))
	var due []claimed
	db.QueryRowsCtx(bg, &due, `SELECT id, kind, payload, attempts, locked_until FROM jobs`)
	a, b := due[0], due[0]
	lease := c.now().Add(time.Minute).UnixMilli()
	okA, _ := w.take(bg, &a, lease)
	okB, _ := w.take(bg, &b, lease+1)
	if !okA || okB || a.Attempts != 1 || a.Locked != lease {
		t.Fatalf("a=%v %+v b=%v", okA, a, okB)
	}
}

func TestFinishedJobsNeverCrowdOutADueOne(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{Concurrency: 1})
	n := 0
	Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { n++; return nil })
	for range 3 {
		w.Enqueue(bg, db, "x", wrapperspb.String(""), At(c.now().Add(-time.Hour)))
	}
	for range 3 {
		w.Tick(bg)
	}
	w.Enqueue(bg, db, "x", wrapperspb.String(""))
	w.Tick(bg)
	if n != 4 {
		t.Fatalf("ran %d, want the new job too", n)
	}
}

func TestADefaultLeaseIsFiveMinutes(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	Handle(w, "x", func(context.Context, *wrapperspb.StringValue) error { return nil })
	w.Enqueue(bg, db, "x", wrapperspb.String(""))
	got, _ := w.claim(bg, 1)
	if len(got) != 1 || got[0].Locked != c.now().Add(5*time.Minute).UnixMilli() {
		t.Fatalf("claimed %+v", got)
	}
}

func TestPlanningMovesPastTheSlotsItEnqueued(t *testing.T) {
	db, c := open(t), start()
	w := worker(t, db, c, Options{})
	sched, _ := ParseSchedule("* * * * *")
	w.Cron("each", sched, func(context.Context, time.Time) error { return nil })
	w.Tick(bg)
	if want := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC); !w.crons[0].planned.Equal(want) {
		t.Fatalf("planned = %v, want %v", w.crons[0].planned, want)
	}
}

type stored struct {
	Kind    string  `db:"kind"`
	Dedup   *string `db:"dedup"`
	Payload string  `db:"payload"`
	RunAt   int64   `db:"run_at"`
	Locked  int64   `db:"locked_until"`
}

func storedIn(t *testing.T, db *data.DB, table string) []stored {
	t.Helper()
	var out []stored
	if err := db.QueryRowsCtx(bg, &out, `SELECT kind, dedup, payload, run_at, locked_until FROM `+table+` ORDER BY kind, run_at`); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestEnqueueWithoutAWorkerWritesTheRowsTheMethodWritesAndWaitsForItsTimeOrNow(t *testing.T) {
	db, c := open(t), start()
	worker(t, db, c, Options{})
	worker(t, db, c, Options{Table: "mirror"})
	queue, err := New(nil, Options{Table: "jobs"})
	if err != nil {
		t.Fatal(err)
	}
	queue.now = c.now
	later := At(c.now().Add(time.Minute))
	for _, enqueue := range []func(kind string, opts ...EnqueueOption) (bool, error){
		func(kind string, opts ...EnqueueOption) (bool, error) {
			return queue.Enqueue(bg, db, kind, wrapperspb.String("ana"), opts...)
		},
		func(kind string, opts ...EnqueueOption) (bool, error) {
			return Enqueue(bg, db, "mirror", kind, wrapperspb.String("ana"), append([]EnqueueOption{At(c.now())}, opts...)...)
		},
	} {
		for _, e := range []struct {
			kind string
			opts []EnqueueOption
			want bool
		}{
			{"a", nil, true},
			{"b", []EnqueueOption{Key("k1"), later}, true},
			{"b", []EnqueueOption{Key("k1")}, false},
		} {
			if ok, err := enqueue(e.kind, e.opts...); ok != e.want || err != nil {
				t.Fatalf("enqueue %s = %v %v, want %v", e.kind, ok, err, e.want)
			}
		}
	}
	method, free := storedIn(t, db, "jobs"), storedIn(t, db, "mirror")
	if len(method) != 2 || method[1].Dedup == nil || !reflect.DeepEqual(method, free) {
		t.Fatalf("the method wrote %+v, Enqueue wrote %+v", method, free)
	}
	before := time.Now().UnixMilli()
	if ok, err := Enqueue(bg, db, "mirror", "now", wrapperspb.String("")); !ok || err != nil {
		t.Fatalf("Enqueue without At = %v %v", ok, err)
	}
	if r := storedIn(t, db, "mirror"); r[len(r)-1].Kind != "now" || r[len(r)-1].RunAt < before || r[len(r)-1].RunAt > time.Now().UnixMilli() {
		t.Fatalf("a job without At = %+v, want it due now", r[len(r)-1])
	}
	if _, err := Enqueue(bg, db, `"mirror"`, "a", wrapperspb.String("")); err == nil {
		t.Error("a table name with SQL in it")
	}
}
