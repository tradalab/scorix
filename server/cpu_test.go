package server

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/tradalab/scorix/kernel"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
)

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

func TestCPUIsReadFromTheProcesssOwnCgroupWithItsTightestQuota(t *testing.T) {
	root := fstest.MapFS{
		"proc/self/cgroup":                        file("0::/kubepods/pod1/c1\n"),
		"sys/fs/cgroup/kubepods/pod1/c1/cpu.stat": file("usage_usec 2500000\nuser_usec 2000000\n"),
		"sys/fs/cgroup/kubepods/pod1/c1/cpu.max":  file("max 100000\n"),
		"sys/fs/cgroup/kubepods/pod1/cpu.max":     file("150000 100000\n"),
		"sys/fs/cgroup/kubepods/cpu.max":          file("300000 100000\n"),
		"sys/fs/cgroup/cpu.stat":                  file("usage_usec 999999999\n"),
	}
	src, err := cpuOf(root, 8)
	if err != nil {
		t.Fatal(err)
	}
	if used, err := src.used(); err != nil || used != 2500*time.Millisecond || src.cores != 1.5 {
		t.Fatalf("used %v %v over %v cores, want 2.5s over 1.5", used, err, src.cores)
	}
	root["sys/fs/cgroup/kubepods/pod1/cpu.max"] = file("max 100000\n")
	root["sys/fs/cgroup/kubepods/cpu.max"] = file("max 100000\n")
	if src, _ := cpuOf(root, 4); src.cores != 4 {
		t.Fatalf("no quota anywhere = %v cores, want GOMAXPROCS 4", src.cores)
	}
	root["sys/fs/cgroup/kubepods/pod1/c1/cpu.stat"] = file("user_usec 1\n")
	if _, err := src.used(); err == nil {
		t.Fatal("a cpu.stat without usage_usec read as usage")
	}
}

func TestWithoutACgroupCPUIsReadFromProcSelfStat(t *testing.T) {
	root := fstest.MapFS{"proc/self/stat": file("4242 (odd) name) S 1 2 3 4 5 6 7 8 9 10 150 50 0 0 20 0 1 0\n")}
	src, err := cpuOf(root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if used, err := src.used(); err != nil || used != 2*time.Second || src.cores != 2 {
		t.Fatalf("used %v %v over %v cores, want 2s over 2", used, err, src.cores)
	}
	root["proc/self/stat"] = file("4242 (x) S 1 2")
	if _, err := src.used(); err == nil {
		t.Fatal("a short stat line read as usage")
	}
	if _, err := cpuOf(fstest.MapFS{}, 2); err == nil {
		t.Fatal("no source at all was accepted")
	}
}

func TestTheSampleIsAMovingAverageOfTheShareUsed(t *testing.T) {
	used := time.Duration(0)
	s := &sampler{src: cpuSource{cores: 2, used: func() (time.Duration, error) { return used, nil }}}
	now := time.Unix(1000, 0)
	s.sample(now)
	if s.load() != 0 {
		t.Fatalf("one sample = %v", s.load())
	}
	for range 200 {
		now = now.Add(time.Second)
		used += 1500 * time.Millisecond
		s.sample(now)
	}
	if math.Abs(s.load()-0.75) > 0.01 {
		t.Fatalf("1.5 of 2 cores for a while = %v, want 0.75", s.load())
	}
	now = now.Add(time.Second)
	s.sample(now)
	if math.Abs(s.load()-0.75*cpuDecay) > 0.01 {
		t.Fatalf("one idle second = %v, want %v", s.load(), 0.75*cpuDecay)
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func warmed(c *clock, cpu *float64) *shedder {
	s := &shedder{threshold: 0.8, cpu: func() float64 { return *cpu }, now: c.now}
	for range 20 {
		done, _ := s.allow()
		c.t = c.t.Add(10 * time.Millisecond)
		done()
	}
	c.t = c.t.Add(bucketWidth)
	return s
}

func TestTheShedderDropsOnlyWhenHotAndOverWhatItCanFinish(t *testing.T) {
	c := &clock{time.Unix(1000, 0).Truncate(bucketWidth)}
	cpu := 0.1
	s := warmed(c, &cpu)
	if limit, ok := s.capacity(c.now()); !ok || math.Abs(limit-1) > 0.01 {
		t.Fatalf("capacity = %v %v, want 10 calls a bucket x 10 buckets a second x 10ms = 1", limit, ok)
	}
	var held []func()
	for range 5 {
		done, ok := s.allow()
		if !ok {
			t.Fatal("a cool server shed a call")
		}
		held = append(held, done)
	}
	cpu = 0.95
	if _, ok := s.allow(); ok {
		t.Fatal("a hot server over its capacity let a call through")
	}
	for _, done := range held {
		done()
	}
	if _, ok := s.allow(); !ok {
		t.Fatal("a hot server under its capacity shed a call")
	}
}

func TestAfterADropTheShedderStaysWaryForACoolOff(t *testing.T) {
	c := &clock{time.Unix(1000, 0).Truncate(bucketWidth)}
	cpu := 0.95
	s := warmed(c, &cpu)
	a, _ := s.allow()
	if _, ok := s.allow(); ok {
		t.Fatal("over capacity while hot was let through")
	}
	cpu = 0.1
	c.t = c.t.Add(coolOff / 2)
	if _, ok := s.allow(); ok {
		t.Fatal("over capacity right after a drop was let through")
	}
	c.t = c.t.Add(coolOff)
	done, ok := s.allow()
	if !ok {
		t.Fatal("cool and past the cool-off, a call was shed")
	}
	a()
	done()
}

func TestAShedderWithNoHistoryShedsNothing(t *testing.T) {
	s := &shedder{threshold: 0.5, cpu: func() float64 { return 1 }, now: time.Now}
	for range 10 {
		if _, ok := s.allow(); !ok {
			t.Fatal("a cold start shed a call")
		}
	}
}

func TestOldBucketsAreForgotten(t *testing.T) {
	c := &clock{time.Unix(1000, 0).Truncate(bucketWidth)}
	cpu := 0.1
	s := warmed(c, &cpu)
	c.t = c.t.Add(bucketCount * bucketWidth)
	if _, ok := s.capacity(c.now()); ok {
		t.Fatal("history older than the window still counts")
	}
}

func TestAServerOutOfCPUShedsWithRetryAfterAndTellsOnShed(t *testing.T) {
	entered, release := make(chan struct{}, 8), make(chan struct{})
	var hold atomic.Bool
	slow := func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
		if hold.Load() {
			entered <- struct{}{}
			<-release
		}
		return in, nil
	}
	m, _ := scorixconnect.NewMux(scorixconnect.Options{Auth: anyone, Timeout: 10 * time.Second})
	scorixconnect.Unary(m, echo, kernel.Rule{}, slow)
	var shed atomic.Int32
	s := New(Options{ShedAboveCPU: 0.5, OnShed: func(*http.Request) { shed.Add(1) }})
	s.root = fstest.MapFS{"proc/self/stat": file("1 (x) S 1 2 3 4 5 6 7 8 9 10 0 0 0 0\n")}
	s.Mount("/test.Echo/", m)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, ln) }()
	var once sync.Once
	free := func() { once.Do(func() { close(release) }) }
	defer func() { free(); cancel(); <-done }()
	conn := scorixconnect.Dial("http://"+ln.Addr().String(), scorixconnect.ConnOptions{})
	callIt := func() error {
		_, err := scorixconnect.Call[wrapperspb.StringValue, wrapperspb.StringValue](context.Background(), conn, echo, wrapperspb.String("x"))
		return err
	}
	for range 30 {
		if err := callIt(); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(bucketWidth + 10*time.Millisecond)
	s.cpu.share.Store(math.Float64bits(0.99))
	hold.Store(true)
	limit, _ := s.load.capacity(time.Now())
	for range int(max(limit, 1)) {
		go callIt()
		<-entered
	}
	hold.Store(false)
	if err := callIt(); connect.CodeOf(err) != connect.CodeUnavailable || shed.Load() != 1 {
		t.Fatalf("a call over capacity while out of CPU = %v, OnShed heard %d", err, shed.Load())
	}
}

func TestShedAboveCPUOutOfRangeFailsRunAndAnUnreadableCPUShedsNothing(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	if err := New(Options{ShedAboveCPU: 1.5}).Run(context.Background(), ln); err == nil || !strings.Contains(err.Error(), "between 0 and 1") {
		t.Fatalf("ShedAboveCPU 1.5 = %v", err)
	}
	var buf bytes.Buffer
	s := New(Options{ShedAboveCPU: 0.5, Log: slog.New(slog.NewTextHandler(&buf, nil))})
	s.root = fstest.MapFS{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.watchCPU(ctx); err != nil || s.cpuOn.Load() || !strings.Contains(buf.String(), "sheds nothing") {
		t.Fatalf("unreadable CPU = %v, on=%v, log %q", err, s.cpuOn.Load(), buf.String())
	}
}

func TestTheBucketStillFillingIsNotHistory(t *testing.T) {
	c := &clock{time.Unix(1000, 0).Truncate(bucketWidth)}
	s := &shedder{threshold: 0.5, cpu: func() float64 { return 1 }, now: c.now}
	for range 5 {
		done, _ := s.allow()
		c.t = c.t.Add(time.Millisecond)
		done()
	}
	if _, ok := s.capacity(c.now()); ok {
		t.Fatal("the current bucket counted before it was complete")
	}
}

func TestAnOpenStreamIsJudgedButNeverCountedAgainstUnaryCalls(t *testing.T) {
	c := &clock{time.Unix(1000, 0).Truncate(bucketWidth)}
	cpu := 0.1
	s := New(Options{ShedAboveCPU: 0.8})
	s.load = warmed(c, &cpu)
	s.cpuOn.Store(true)
	release := make(chan struct{})
	var entered sync.WaitGroup
	h := s.shed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/proto" {
			entered.Done()
			<-release
		}
	}))
	send := func(ct string) int {
		r := httptest.NewRequest("POST", "/x", nil)
		r.Header.Set("Content-Type", ct)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	var streams sync.WaitGroup
	for _, ct := range []string{"application/connect+proto", "application/grpc", "application/grpc+proto", "application/connect+json"} {
		entered.Add(1)
		streams.Add(1)
		go func() { defer streams.Done(); send(ct) }()
	}
	entered.Wait()
	cpu = 0.95
	if code := send("application/proto"); code != http.StatusOK {
		t.Fatalf("a unary call behind 4 open streams = %d", code)
	}
	close(release)
	streams.Wait()
	s.load.inflight.Add(1)
	if code := send("application/grpc"); code != http.StatusServiceUnavailable {
		t.Fatalf("a stream opened while hot and over capacity = %d", code)
	}
}
