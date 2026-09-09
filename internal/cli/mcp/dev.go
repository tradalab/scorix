package mcp

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tradalab/scorix/internal/cli/runner"
	"github.com/tradalab/scorix/proc"
)

// The dev loop runs as a child rather than in this process: it never returns on
// its own, it prints from everywhere, and proc already carries the Job Object /
// Pdeathsig wiring that keeps it from outliving the agent that started it.
type devJob struct {
	mu      sync.Mutex
	process *proc.Process
	since   time.Time
	command string
	dir     string
}

type devResult struct {
	Running bool     `json:"running"`
	PID     int      `json:"pid,omitempty"`
	Command string   `json:"command,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Uptime  string   `json:"uptime,omitempty"`
	Exit    string   `json:"exit,omitempty"`
	Log     []string `json:"log,omitempty"`
}

func (j *devJob) start(ctx context.Context, a args, out io.Writer) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if j.process != nil && !exited(j.process) {
		return runner.EmitJSON(out, "dev start", j.snapshot(40),
			errors.New("a dev job is already running; stop it first"))
	}

	exe, err := os.Executable()
	if err != nil {
		return runner.EmitJSON(out, "dev start", nil, err)
	}
	// The child runs IN the project dir and is given no --dir: passing both made a
	// relative "myapp" resolve twice, into myapp/myapp, and the error named a
	// directory that never existed.
	dir := a.str("dir", ".")
	argv := []string{"dev"}
	if url := a.str("url", ""); url != "" {
		argv = append(argv, "--url", url)
	}
	if a.truthy("legacy") {
		argv = append(argv, "--legacy")
	}
	if !a.truthyOr("watch", true) {
		argv = append(argv, "--watch=false")
	}

	// No restart policy: a dev loop that keeps dying is the answer the caller
	// needs, and relaunching it would bury the compile error that killed it.
	p, err := proc.Start(ctx, proc.Spec{
		Path:     exe,
		Args:     argv,
		Dir:      dir,
		LogLines: 400,
		Stderr:   os.Stdout, // already pointed at stderr for the whole session
	})
	if err != nil {
		return runner.EmitJSON(out, "dev start", nil, err)
	}
	j.process, j.since, j.command, j.dir = p, time.Now(), "scorix "+strings.Join(argv, " "), dir
	return runner.EmitJSON(out, "dev start", j.snapshot(0), nil)
}

func (j *devJob) stop(ctx context.Context, out io.Writer) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.process == nil {
		return runner.EmitJSON(out, "dev stop", &devResult{}, errors.New("no dev job is running"))
	}
	err := j.process.Stop(ctx)
	res := j.snapshot(20)
	res.Running = false
	j.process = nil
	return runner.EmitJSON(out, "dev stop", res, err)
}

func (j *devJob) status(a args, out io.Writer) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return runner.EmitJSON(out, "dev status", j.snapshot(a.num("lines", 40)), nil)
}

// Caller holds j.mu.
func (j *devJob) snapshot(lines int) *devResult {
	res := &devResult{}
	if j.process == nil {
		return res
	}
	res.Command, res.Dir, res.PID = j.command, j.dir, j.process.PID()
	if exited(j.process) {
		if err := j.process.Err(); err != nil {
			res.Exit = err.Error()
		} else {
			res.Exit = "exited"
		}
	} else {
		res.Running = true
		res.Uptime = time.Since(j.since).Round(time.Second).String()
	}
	if lines > 0 {
		log := j.process.Logs()
		if len(log) > lines {
			log = log[len(log)-lines:]
		}
		res.Log = log
	}
	return res
}

func exited(p *proc.Process) bool {
	select {
	case <-p.Done():
		return true
	default:
		return false
	}
}
