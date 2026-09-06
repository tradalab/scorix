package diag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tradalab/scorix/logger"
)

const (
	pendingName  = "fatal.pending"
	defaultKeep  = 10
	reportPrefix = "crash-"
)

// Dir keeps the subdirectory name in one place: the app writes reports there,
// the CLI looks for them there.
func Dir(dataDir string) string { return filepath.Join(dataDir, "crash") }

type Options struct {
	Dir     string // where reports land; usually <data dir>/crash
	App     string
	Version string
	Keep    int // reports to keep, oldest pruned first (default 10)
}

type Report struct {
	Time    time.Time `json:"time"`
	App     string    `json:"app,omitempty"`
	Version string    `json:"version,omitempty"`
	OS      string    `json:"os"`
	Arch    string    `json:"arch"`
	Go      string    `json:"go"`
	// Kind is "panic" for one the framework recovered from, "fatal" for one that
	// killed the process and was picked up on the next start.
	Kind  string   `json:"kind"`
	Where string   `json:"where,omitempty"`
	Panic string   `json:"panic,omitempty"`
	Stack string   `json:"stack"`
	Log   []string `json:"log,omitempty"`
	// LogAvailable is false for a fatal crash: the ring lived in the dead
	// process, so its absence is a fact about the crash, not a missing field.
	LogAvailable bool `json:"logAvailable"`
}

var (
	seq  atomic.Uint64
	mu   sync.Mutex
	opts Options
	on   bool
)

// Init is optional: skip it and every other call in this package no-ops.
func Init(o Options) error {
	if o.Dir == "" {
		return fmt.Errorf("diag: Dir is required")
	}
	if o.Keep <= 0 {
		o.Keep = defaultKeep
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return err
	}

	mu.Lock()
	opts, on = o, true
	mu.Unlock()

	promotePending(o)

	// The runtime dups the fd, so closing our handle here keeps nothing open.
	f, err := os.Create(filepath.Join(o.Dir, pendingName))
	if err != nil {
		return err
	}
	defer f.Close()
	return debug.SetCrashOutput(f, debug.CrashOptions{})
}

// disarm releases the crash file. The runtime holds a duplicate of the handle for
// the life of the process, and on Windows that blocks deleting its directory.
func disarm() {
	_ = debug.SetCrashOutput(nil, debug.CrashOptions{})
	mu.Lock()
	on = false
	mu.Unlock()
}

func Panic(where string, r any, stack []byte) {
	mu.Lock()
	o, armed := opts, on
	mu.Unlock()
	if !armed {
		return
	}
	if stack == nil {
		stack = debug.Stack()
	}
	log := logger.Tail()
	write(o, Report{
		Time: time.Now(), App: o.App, Version: o.Version,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version(),
		Kind: "panic", Where: where, Panic: fmt.Sprint(r),
		Stack: string(stack), Log: log, LogAvailable: true,
	})
}

// Recover does NOT re-panic: every caller today already swallowed the panic, and
// silently changing that would take an app down where it used to survive.
func Recover(where string) {
	if r := recover(); r != nil {
		Panic(where, r, debug.Stack())
		logger.Error(fmt.Sprintf("panic recovered in %s: %v", where, r))
	}
}

// promotePending turns the traceback the runtime wrote as the process died into
// a report, on the next start - the only moment there is code running to do it.
func promotePending(o Options) {
	path := filepath.Join(o.Dir, pendingName)
	b, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(b))) == 0 {
		return
	}
	write(o, Report{
		Time: modTimeOr(path, time.Now()), App: o.App, Version: o.Version,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version(),
		Kind: "fatal", Panic: firstLine(string(b)), Stack: string(b),
		LogAvailable: false,
	})
	_ = os.Remove(path)
}

func write(o Options, rep Report) {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return
	}
	// A crash storm inside one millisecond would write over itself; zero-padded
	// because List sorts these names as strings, where "10" precedes "9".
	name := fmt.Sprintf("%s%s-%04d-%s.json", reportPrefix,
		rep.Time.UTC().Format("20060102-150405.000"), seq.Add(1)%10000, rep.Kind)
	if err := os.WriteFile(filepath.Join(o.Dir, name), append(b, '\n'), 0o644); err != nil {
		logger.Error(fmt.Sprintf("diag: cannot write crash report: %v", err))
		return
	}
	prune(o)
}

// prune stops a crash loop from filling the data dir with the same stack.
func prune(o Options) {
	names, err := List(o.Dir)
	if err != nil || len(names) <= o.Keep {
		return
	}
	for _, p := range names[o.Keep:] {
		_ = os.Remove(p)
	}
}

// List returns report paths, newest first.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), reportPrefix) || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out))) // the timestamp in the name sorts as the clock does
	return out, nil
}

func modTimeOr(path string, fallback time.Time) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return fallback
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
