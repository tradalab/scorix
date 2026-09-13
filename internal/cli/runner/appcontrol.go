package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/tradalab/scorix/internal/devctl"
	"github.com/tradalab/scorix/module"
)

type AppControlOptions struct {
	Dir     string
	Op      string
	Args    map[string]any
	Timeout time.Duration
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

type appControlResult struct {
	Op   string          `json:"op"`
	PID  int             `json:"pid,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// AppControl drives an app that is already running: read the address it
// published, dial loopback, ask for one operation. "Not found" is the normal
// answer for a plain `go run` or a shipped build, not a fault.
func AppControl(ctx context.Context, opt AppControlOptions) error {
	res, err := appControlCall(ctx, opt)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "app "+opt.Op, res, err)
	}
	if err != nil {
		return err
	}
	fmt.Printf("==> app %s (pid %d)\n%s\n", res.Op, res.PID, string(res.Data))
	return nil
}

func appControlCall(ctx context.Context, opt AppControlOptions) (*appControlResult, error) {
	dir := opt.Dir
	if dir == "" {
		dir = "."
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return appControlAt(ctx, AppControlPath(root), opt)
}

// Split from the path resolution so a test can drive the wire against a control
// file it wrote, instead of the real per-user data dir.
func appControlAt(ctx context.Context, path string, opt AppControlOptions) (*appControlResult, error) {
	f, err := readDevControlFile(path)
	if err != nil {
		return nil, err
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", f.Addr)
	if err != nil {
		// The file outlives a crash, so a refused dial is the common case.
		return nil, fmt.Errorf("the app is not listening on %s: %w (stale %s - is the app running with %s set?)", f.Addr, err, path, devctl.Env)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	req := map[string]any{"token": f.Token, "op": opt.Op, "timeout_ms": timeout.Milliseconds()}
	if len(opt.Args) > 0 {
		req["args"] = opt.Args
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}

	var res struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
		Code  string          `json:"code"`
	}
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		return nil, fmt.Errorf("app %s: no answer: %w", opt.Op, err)
	}
	if !res.OK {
		if res.Code != "" {
			return nil, fmt.Errorf("app %s: %s (%s)", opt.Op, res.Error, res.Code)
		}
		return nil, fmt.Errorf("app %s: %s", opt.Op, res.Error)
	}
	return &appControlResult{Op: opt.Op, PID: f.PID, Data: res.Data}, nil
}

// Reads the `app:` block, the same one the running app reads from its embedded
// manifest. Deriving it from the top-level `name:` instead would point at another
// directory the moment someone edits one of the two.
func AppControlPath(root string) string {
	name := ""
	if cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml")); err == nil && cfg != nil && cfg.App != nil {
		name = cfg.App.Name
		if name == "" {
			name = cfg.App.Identifier
		}
	}
	return filepath.Join(module.DataDir(name), devctl.FileName)
}

func readDevControlFile(path string) (devctl.File, error) {
	var f devctl.File
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return f, fmt.Errorf("no app is exposing a control socket (looked at %s); start it with %s=1, which scorix_dev_start does", path, devctl.Env)
		}
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return f, fmt.Errorf("control file %s is not readable: %w", path, err)
	}
	if f.Addr == "" || f.Token == "" {
		return f, fmt.Errorf("control file %s carries no address", path)
	}
	return f, nil
}
