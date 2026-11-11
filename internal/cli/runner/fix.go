package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/tradalab/scorix/idl"
)

type FixResult struct {
	Proto   string   `json:"proto"`
	Changed bool     `json:"changed"`
	Notes   []string `json:"notes,omitempty"`
}

type FixOptions struct {
	Dir string
	// Proto fixes this one file instead of the project's, for an idl repo with no scorix.yaml.
	Proto string
	// Check reports what would change and fails with ExitDrift instead of writing.
	Check   bool
	JSONOut io.Writer
}

// Fix moves the proto's comment annotations into their scorix.v1 options in place.
func Fix(ctx context.Context, opt FixOptions) error {
	res := &FixResult{}
	err := fix(opt, res)
	for _, n := range res.Notes {
		fmt.Println("note:", n)
	}
	if err == nil && res.Changed && !opt.Check {
		fmt.Println("==> moved the comment annotations of", res.Proto, "into options")
	}
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "fix", res, err)
	}
	return err
}

func fix(opt FixOptions, res *FixResult) error {
	path, rel := opt.Proto, opt.Proto
	if path == "" {
		root := opt.Dir
		if root == "" {
			root = "."
		}
		cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
		if err != nil {
			return err
		}
		rel = cfg.Proto
		if rel == "" {
			rel = DefaultProtoPath
		}
		path = filepath.Join(root, rel)
	}
	res.Proto = rel
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, notes, err := idl.Fix(filepath.Base(path), string(src))
	if err != nil {
		return err
	}
	res.Notes, res.Changed = notes, out != string(src)
	switch {
	case !res.Changed:
		return nil
	case opt.Check:
		return exitErrorf(ExitDrift, "drift", "%s still annotates with comments; run scorix fix", rel)
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
