package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type TestOptions struct {
	Dir     string
	Tags    []string
	Run     string // -run pattern
	Race    bool
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

type TestFailure struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	Output  string `json:"output,omitempty"`
}

type TestResult struct {
	// The argv actually run. An app's own Makefile may ask for more - pchub's
	// `make test` adds -race - so a caller can see what this run did and did not
	// cover instead of assuming the two are the same check.
	Command     string            `json:"command"`
	Passed      int               `json:"passed"`
	Failed      int               `json:"failed"`
	Skipped     int               `json:"skipped"`
	Failures    []TestFailure     `json:"failures,omitempty"`
	Diagnostics []BuildDiagnostic `json:"diagnostics,omitempty"` // compile errors, same shape as build
}

type LintOptions struct {
	Dir     string
	Tags    []string
	JSONOut io.Writer
}

type LintResult struct {
	Command  string            `json:"command"`
	Findings []BuildDiagnostic `json:"findings,omitempty"`
}

// Exists so an agent never drops to a raw shell for the step it repeats most: the
// edit/generate/build loop is only half a loop without "did it break anything".
func Test(ctx context.Context, opt TestOptions) error {
	res := &TestResult{}
	err := runTests(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "test", res, err)
	}
	return err
}

func runTests(ctx context.Context, opt TestOptions, res *TestResult) error {
	root, tags, err := checkContext(opt.Dir, opt.Tags)
	if err != nil {
		return err
	}
	args := []string{"test", "-json"}
	if opt.Race {
		args = append(args, "-race")
	}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	if opt.Run != "" {
		args = append(args, "-run", opt.Run)
	}
	args = append(args, "./...")

	res.Command = "go " + strings.Join(args, " ")
	fmt.Printf("==> %s\n", res.Command)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stdout, &stderr)
	if err := cmd.Start(); err != nil {
		return err
	}
	collectTestEvents(res, stdout)
	runErr := cmd.Wait()
	if res.Failed > 0 || len(res.Diagnostics) > 0 {
		return &ExitError{Code: ExitFailed, Kind: "failed",
			Err: fmt.Errorf("%d test(s) failed", res.Failed)}
	}
	return toolchainError(runErr, stderr.String())
}

// collectTestEvents keeps the failing output and nothing else: an agent handed
// every passing line has to filter it, and the whole point is that it does not.
func collectTestEvents(res *TestResult, r io.Reader) {
	type event struct {
		Action     string
		Package    string
		ImportPath string // build-output events carry the package here, not in Package
		Test       string
		Output     string
	}
	failing := map[string]*strings.Builder{}
	failedPkgs := map[string]bool{}
	dec := json.NewDecoder(r)
	for {
		var ev event
		// Anything the toolchain says outside this stream is on stderr: measured,
		// `go test -json` never puts non-JSON here, not even for a broken go.mod.
		if err := dec.Decode(&ev); err != nil {
			break
		}
		key := ev.Package + "\x00" + ev.Test
		switch ev.Action {
		case "build-output":
			// A package that does not compile has no tests to report, so this is the
			// only place the caller learns what is actually wrong.
			fmt.Print(ev.Output)
			for _, line := range strings.Split(strings.TrimRight(ev.Output, "\n"), "\n") {
				if d := parseGoDiagnostic(ev.ImportPath, line); d.File != "" {
					res.Diagnostics = append(res.Diagnostics, d)
				}
			}
		case "output":
			fmt.Print(ev.Output)
			if b := failing[key]; b != nil {
				b.WriteString(ev.Output)
			} else {
				b := &strings.Builder{}
				b.WriteString(ev.Output)
				failing[key] = b
			}
		case "pass":
			if ev.Test != "" {
				res.Passed++
			}
			delete(failing, key)
		case "skip":
			if ev.Test != "" {
				res.Skipped++
			}
			delete(failing, key)
		case "fail":
			if ev.Test != "" {
				res.Failed++
			}
			out := ""
			if b := failing[key]; b != nil {
				out = strings.TrimSpace(b.String())
			}
			// The package's own fail event carries nothing but "FAIL <pkg>": keeping it
			// makes len(failures) disagree with failed, dropping it loses the package
			// that did not compile, where it is the only event there is.
			if ev.Test == "" && failedPkgs[ev.Package] {
				delete(failing, key)
				continue
			}
			if ev.Test != "" {
				failedPkgs[ev.Package] = true
			}
			res.Failures = append(res.Failures, TestFailure{Package: ev.Package, Test: ev.Test, Output: out})
			delete(failing, key)
		}
	}
}

// Same embed guarantee and envelope as Test, so a caller reads findings[] instead
// of scraping two text formats.
func Lint(ctx context.Context, opt LintOptions) error {
	res := &LintResult{}
	err := runLint(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "lint", res, err)
	}
	return err
}

func runLint(ctx context.Context, opt LintOptions, res *LintResult) error {
	root, tags, err := checkContext(opt.Dir, opt.Tags)
	if err != nil {
		return err
	}
	args := []string{"vet", "-json"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./...")

	res.Command = "go " + strings.Join(args, " ")
	fmt.Printf("==> %s\n", res.Command)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = root
	var out, stderr strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = io.MultiWriter(os.Stdout, &stderr)
	runErr := cmd.Run()

	res.Findings = parseVetJSON(out.String())
	if len(res.Findings) > 0 {
		return &ExitError{Code: ExitFailed, Kind: "failed",
			Err: fmt.Errorf("%d vet finding(s)", len(res.Findings))}
	}
	return toolchainError(runErr, stderr.String())
}

// go vet -json writes a bare `{}` before the real document, so the values are
// read in sequence rather than unmarshalled once.
func parseVetJSON(s string) []BuildDiagnostic {
	dec := json.NewDecoder(strings.NewReader(s))
	var found []BuildDiagnostic
	for {
		var doc map[string]map[string][]struct {
			Posn    string `json:"posn"`
			Message string `json:"message"`
		}
		if err := dec.Decode(&doc); err != nil {
			return found
		}
		for pkg, analyzers := range doc {
			for name, items := range analyzers {
				for _, it := range items {
					d := parseGoDiagnostic(pkg, it.Posn+": "+name+": "+it.Message)
					found = append(found, d)
				}
			}
		}
	}
}

// The embed directory has to exist first: `//go:embed all:.scorix/dist` must
// compile before `go test` or `go vet` can say anything at all.
func checkContext(dir string, extraTags []string) (root string, tags []string, err error) {
	if dir == "" {
		dir = "."
	}
	root, err = filepath.Abs(dir)
	if err != nil {
		return "", nil, err
	}
	cfg, err := loadProjectConfig(filepath.Join(root, "scorix.yaml"))
	if err != nil {
		return "", nil, fmt.Errorf("load scorix.yaml: %w", err)
	}
	if cfg.Build != nil {
		tags = append(tags, cfg.Build.Tags...)
	}
	tags = append(tags, extraTags...)
	if err := ensureEmbedDir(filepath.Join(root, ".scorix", "dist")); err != nil {
		return "", nil, err
	}
	return root, tags, nil
}

// Go puts compile diagnostics on its JSON stream but reports its own failures - a
// broken go.mod, an unresolvable module - only on stderr, so without this the
// envelope carries an exit code and nothing to act on.
func toolchainError(err error, stderr string) error {
	if err == nil {
		return nil
	}
	var kept []string
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return err
	}
	// The last lines, not the first: go prints the headline then the detail.
	if len(kept) > 4 {
		kept = kept[len(kept)-4:]
	}
	return fmt.Errorf("%s", strings.Join(kept, "; "))
}
