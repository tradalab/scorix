package proc

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/logger"
)

type Spec struct {
	Path string // executable; bare names resolve via PATH
	Args []string
	Dir  string
	Env  []string // nil inherits the parent environment

	Health       func(ctx context.Context) error
	ReadyTimeout time.Duration // default 60s (model loads are slow on first run)
	PollInterval time.Duration // default 300ms

	Restart  RestartPolicy // zero value: never restart
	LogLines int           // stdout+stderr ring size; default 100

	OnExit func(err error, willRestart bool)
}

type RestartPolicy struct {
	MaxRestarts int           // consecutive relaunches before giving up; 0 = never restart
	Backoff     time.Duration // first delay, doubling per consecutive failure, capped at 30s
}

const restartResetAfter = time.Minute

type Process struct {
	spec Spec

	mu       sync.Mutex
	cmd      *exec.Cmd
	logs     *ring
	life     *lifetime
	stopped  bool
	finalErr error

	stopOnce sync.Once
	stopCh   chan struct{} // closed by Stop; interrupts the restart backoff
	done     chan struct{} // closed when supervision ends (no further restarts)
}

func Start(ctx context.Context, spec Spec) (*Process, error) {
	if spec.Path == "" {
		return nil, fault.New("invalid_spec", "proc: Spec.Path is empty")
	}
	if spec.ReadyTimeout == 0 {
		spec.ReadyTimeout = 60 * time.Second
	}
	if spec.PollInterval == 0 {
		spec.PollInterval = 300 * time.Millisecond
	}
	if spec.LogLines == 0 {
		spec.LogLines = 100
	}
	p := &Process{spec: spec, logs: newRing(spec.LogLines), stopCh: make(chan struct{}), done: make(chan struct{})}

	exited, err := p.launch(ctx)
	if err != nil {
		return nil, err
	}
	go p.supervise(exited)
	return p, nil
}

func (p *Process) launch(ctx context.Context) (chan error, error) {
	cmd := exec.Command(p.spec.Path, p.spec.Args...)
	cmd.Dir = p.spec.Dir
	cmd.Env = p.spec.Env
	configureSysProc(cmd)

	if pipe, err := cmd.StdoutPipe(); err == nil {
		go drainToRing(pipe, p.logs)
	}
	if pipe, err := cmd.StderrPipe(); err == nil {
		go drainToRing(pipe, p.logs)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proc: start %s: %w", p.spec.Path, err)
	}

	p.mu.Lock()
	p.cmd = cmd
	if p.life == nil {
		p.life = newLifetime()
	}
	life := p.life
	p.mu.Unlock()
	life.attach(cmd)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	if p.spec.Health == nil {
		return exited, nil
	}

	deadline := time.Now().Add(p.spec.ReadyTimeout)
	for {
		hctx, cancel := context.WithTimeout(ctx, p.spec.PollInterval*4)
		herr := p.spec.Health(hctx)
		cancel()
		if herr == nil {
			return exited, nil
		}
		select {
		case werr := <-exited:
			return nil, fmt.Errorf("proc: %s exited before becoming healthy (%v)\n%s", p.spec.Path, werr, p.logsTail())
		case <-ctx.Done():
			p.terminate()
			<-exited
			return nil, ctx.Err()
		default:
		}
		if time.Now().After(deadline) {
			p.terminate()
			<-exited
			return nil, fmt.Errorf("proc: %s not healthy within %s\n%s", p.spec.Path, p.spec.ReadyTimeout, p.logsTail())
		}
		select {
		case <-time.After(p.spec.PollInterval):
		case <-ctx.Done():
			p.terminate()
			<-exited
			return nil, ctx.Err()
		}
	}
}

func (p *Process) supervise(exited chan error) {
	restarts := 0
	launchedAt := time.Now()
	for {
		werr := <-exited

		p.mu.Lock()
		stopped := p.stopped
		p.mu.Unlock()
		if stopped {
			p.finish(nil)
			return
		}

		if time.Since(launchedAt) > restartResetAfter {
			restarts = 0
		}
		willRestart := restarts < p.spec.Restart.MaxRestarts
		if p.spec.OnExit != nil {
			p.spec.OnExit(werr, willRestart)
		}
		if !willRestart {
			p.finish(werr)
			return
		}

		backoff := p.spec.Restart.Backoff
		if backoff <= 0 {
			backoff = time.Second
		}
		backoff = min(backoff<<restarts, 30*time.Second)
		restarts++
		logger.Warn("proc: child exited — restarting", "path", p.spec.Path, "attempt", restarts, "backoff", backoff.String(), "err", errStr(werr))
		select {
		case <-time.After(backoff):
		case <-p.stopCh: // Stop must not wait out a 30s backoff
			p.finish(nil)
			return
		}

		p.mu.Lock()
		stopped = p.stopped
		p.mu.Unlock()
		if stopped {
			p.finish(nil)
			return
		}

		var err error
		exited, err = p.launch(context.Background())
		if err != nil {
			p.mu.Lock()
			stopped = p.stopped
			p.mu.Unlock()
			if stopped { // Stop killed the relaunch mid-health-wait: not a failure
				p.finish(nil)
				return
			}
			if p.spec.OnExit != nil {
				p.spec.OnExit(err, false)
			}
			p.finish(err)
			return
		}
		launchedAt = time.Now()
	}
}

func (p *Process) finish(err error) {
	p.mu.Lock()
	p.finalErr = err
	life := p.life
	p.life = nil
	p.mu.Unlock()
	life.release()
	close(p.done)
}

func (p *Process) Stop(ctx context.Context) error {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.stopOnce.Do(func() { close(p.stopCh) })
	p.terminate()
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Process) terminate() {
	p.mu.Lock()
	cmd := p.cmd
	life := p.life
	p.mu.Unlock()
	terminateTree(life, cmd)
}

func (p *Process) Done() <-chan struct{} { return p.done }

func (p *Process) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finalErr
}

func (p *Process) PID() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Pid
	}
	return 0
}

func (p *Process) Logs() []string { return p.logs.lines() }

func (p *Process) logsTail() string { return strings.Join(p.logs.lines(), "\n") }

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type ring struct {
	mu    sync.Mutex
	buf   []string
	start int
	n     int
}

func newRing(max int) *ring { return &ring{buf: make([]string, max)} }

func (r *ring) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = line
		r.n++
		return
	}
	r.buf[r.start] = line
	r.start = (r.start + 1) % len(r.buf)
}

func (r *ring) lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, r.n)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(r.start+i)%len(r.buf)])
	}
	return out
}

func drainToRing(pipe io.Reader, r *ring) {
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for sc.Scan() {
		r.add(sc.Text())
	}
}
