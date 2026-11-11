package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/idl"
)

type BreakingChange struct {
	Level string `json:"level"`
	Where string `json:"where"`
	What  string `json:"what"`
}

type BreakingResult struct {
	Before  string           `json:"before"`
	After   string           `json:"after"`
	Level   string           `json:"level"`
	Changes []BreakingChange `json:"changes"`
}

type BreakingOptions struct {
	// Before is a file, or REV:PATH read with git show when no such file exists.
	Before  string
	After   string
	Level   string
	JSONOut io.Writer
}

// Breaking compares two versions of a proto and fails with ExitBreaking when the newer
// one breaks a caller at Level or below; a compile error on either side is ExitFailed.
func Breaking(ctx context.Context, opt BreakingOptions) error {
	res := &BreakingResult{Before: opt.Before, After: opt.After, Level: opt.Level, Changes: []BreakingChange{}}
	err := breaking(ctx, opt, res)
	for _, c := range res.Changes {
		fmt.Printf("%s: %s %s\n", c.Level, c.Where, c.What)
	}
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "breaking", res, err)
	}
	return err
}

func breaking(ctx context.Context, opt BreakingOptions, res *BreakingResult) error {
	level, err := idl.ParseLevel(opt.Level)
	if err != nil {
		return UsageError(err)
	}
	before, err := readRevision(ctx, opt.Before)
	if err != nil {
		return err
	}
	after, err := os.ReadFile(opt.After)
	if err != nil {
		return err
	}
	changes, err := idl.Breaking(filepath.Base(opt.After), before, string(after))
	if err != nil {
		return err
	}
	for _, c := range changes {
		if c.Level <= level {
			res.Changes = append(res.Changes, BreakingChange{Level: c.Level.String(), Where: c.Where, What: c.What})
		}
	}
	if n := len(res.Changes); n > 0 {
		return exitErrorf(ExitBreaking, "breaking", "%d breaking change(s) at level %s or below", n, level)
	}
	return nil
}

func readRevision(ctx context.Context, spec string) (string, error) {
	b, err := os.ReadFile(spec)
	if err == nil {
		return string(b), nil
	}
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(spec, ":") {
		return "", err
	}
	out, gerr := exec.CommandContext(ctx, "git", "show", spec).Output()
	if gerr != nil {
		var ee *exec.ExitError
		if errors.As(gerr, &ee) {
			return "", fmt.Errorf("%s is no file, and git show %s: %s", spec, spec, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", gerr
	}
	return string(out), nil
}
