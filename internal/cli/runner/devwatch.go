package runner

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/tradalab/scorix/proc"
)

const (
	devPollInterval = 500 * time.Millisecond
	devDebounce     = 250 * time.Millisecond
	devStopTimeout  = 5 * time.Second
)

var devSkipDirs = map[string]bool{
	"shell": true, ".scorix": true, "node_modules": true, ".git": true,
	"vendor": true, "dist": true, "testdata": true,
}

type fingerprint struct {
	mod  int64
	size int64
}

type watchSet struct { // polled by mtime+size: no fsnotify dep, and it survives editors that replace files
	root  string
	extra map[string]bool
	seen  map[string]fingerprint
}

func newWatchSet(root string, extra ...string) *watchSet {
	w := &watchSet{root: root, extra: map[string]bool{}, seen: map[string]fingerprint{}}
	for _, e := range extra {
		if e != "" {
			w.extra[filepath.Clean(e)] = true
		}
	}
	return w
}

func (w *watchSet) watched(path string) bool {
	if w.extra[path] {
		return true
	}
	base := filepath.Base(path)
	switch base {
	case "go.mod", "go.sum":
		return true
	}
	return strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go")
}

func (w *watchSet) scan() []string {
	cur := map[string]fingerprint{}
	_ = filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != w.root && devSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !w.watched(path) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		cur[path] = fingerprint{mod: info.ModTime().UnixNano(), size: info.Size()}
		return nil
	})
	for e := range w.extra {
		if _, ok := cur[e]; ok {
			continue
		}
		if info, err := os.Stat(e); err == nil && !info.IsDir() {
			cur[e] = fingerprint{mod: info.ModTime().UnixNano(), size: info.Size()}
		}
	}

	var changed []string
	for path, fp := range cur {
		if old, ok := w.seen[path]; !ok || old != fp {
			changed = append(changed, w.rel(path))
		}
	}
	for path := range w.seen {
		if _, ok := cur[path]; !ok {
			changed = append(changed, w.rel(path))
		}
	}
	w.seen = cur
	return changed
}

func (w *watchSet) rel(path string) string {
	if r, err := filepath.Rel(w.root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(path)
}

type devHooks struct {
	regenerate func(proto, schema bool) error
	build      func() error
	restart    func() error
	appDone    func() <-chan struct{}
	poll       time.Duration
	debounce   time.Duration
	out        io.Writer
}

func devLoop(ctx context.Context, ws *watchSet, protoRel, schemaRel string, h devHooks) error {
	if h.poll == 0 {
		h.poll = devPollInterval
	}
	if h.debounce == 0 {
		h.debounce = devDebounce
	}
	if h.out == nil {
		h.out = io.Discard
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.appDone():
			return nil
		case <-time.After(h.poll):
		}
		changed := ws.scan()
		if len(changed) == 0 {
			continue
		}
		for {
			time.Sleep(h.debounce)
			more := ws.scan()
			if len(more) == 0 {
				break
			}
			changed = append(changed, more...)
		}
		fmt.Fprintf(h.out, "==> [watch] changed: %s\n", summarizeChanged(changed))

		var proto, schema bool
		for _, c := range changed {
			proto = proto || c == protoRel
			schema = schema || c == schemaRel
		}
		if proto || schema {
			if err := h.regenerate(proto, schema); err != nil {
				fmt.Fprintf(h.out, "==> [watch] codegen failed - keeping the previous app running\n%v\n", err)
				continue
			}
			ws.scan() // generated files must not retrigger the loop
		}

		start := time.Now()
		if err := h.build(); err != nil { // a typo must not kill the window the developer is looking at
			fmt.Fprintf(h.out, "==> [watch] build failed - keeping the previous app running\n")
			continue
		}
		if err := h.restart(); err != nil {
			fmt.Fprintf(h.out, "==> [watch] restart failed: %v\n", err)
			continue
		}
		fmt.Fprintf(h.out, "==> [watch] restarted in %s\n", time.Since(start).Round(100*time.Millisecond))
	}
}

func summarizeChanged(changed []string) string {
	if len(changed) <= 3 {
		return strings.Join(changed, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(changed[:3], ", "), len(changed)-3)
}

func devGoBuild(ctx context.Context, root, out string, tags []string, w io.Writer) error {
	args := []string{"build", "-o", out}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, ".")
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	cmd.Stdout = w
	cmd.Stderr = w
	cmd.Env = os.Environ()
	return cmd.Run()
}

func devBinary(root string, gen int) string {
	name := fmt.Sprintf("app-%d", gen%2) // two slots: Windows cannot overwrite a running exe, and the old app runs until the new build succeeds
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, ".scorix", "dev", name)
}

type devApp struct {
	ctx  context.Context
	root string
	env  []string
	tags []string
	gen  int
	cur  *proc.Process
	done chan struct{} // never closes: stands in for Done() while no app runs
}

func (d *devApp) build() error {
	return devGoBuild(d.ctx, d.root, devBinary(d.root, d.gen+1), d.tags, os.Stdout)
}

func (d *devApp) restart() error {
	if d.cur != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), devStopTimeout)
		_ = d.cur.Stop(stopCtx)
		cancel()
		d.cur = nil
	}
	d.gen++
	p, err := proc.Start(d.ctx, proc.Spec{
		Path:     devBinary(d.root, d.gen),
		Args:     []string{"-mode", "app"},
		Dir:      d.root,
		Env:      d.env,
		Stdout:   os.Stdout,
		Stderr:   os.Stderr,
		LogLines: 1,
	})
	if err != nil {
		return err
	}
	d.cur = p
	return nil
}

func (d *devApp) appDone() <-chan struct{} {
	if d.cur == nil {
		return d.done
	}
	return d.cur.Done()
}

func (d *devApp) stop() {
	if d.cur != nil {
		stopCtx, cancel := context.WithTimeout(context.Background(), devStopTimeout)
		_ = d.cur.Stop(stopCtx)
		cancel()
	}
}
