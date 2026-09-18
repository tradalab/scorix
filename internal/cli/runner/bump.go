package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/mod/semver"
)

const scorixRepo = "https://github.com/tradalab/scorix"

type BumpPin struct {
	File string `json:"file"`
	Line int    `json:"line"`
	// module | go | workflow | install | floor
	Kind string `json:"kind"`
	Was  string `json:"was"`
	Now  string `json:"now,omitempty"`
}

type BumpStep struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type BumpResult struct {
	Version   string    `json:"version,omitempty"`
	GoVersion string    `json:"go_version,omitempty"`
	CLI       string    `json:"cli,omitempty"`
	Check     bool      `json:"check"`
	Pins      []BumpPin `json:"pins,omitempty"`
	// Reported, never rewritten: a floor says at least this, not exactly this.
	Floors []BumpPin  `json:"floors,omitempty"`
	Steps  []BumpStep `json:"steps,omitempty"`
	// Ready to paste into the app's STATUS.md.
	Note string `json:"note,omitempty"`
}

type BumpOptions struct {
	Dir     string
	Version string
	Check   bool
	JSONOut io.Writer
}

// Test seams: the real ones talk to the proxy, the git remote and the generators.
var (
	bumpGo     = execTool
	bumpGit    = execTool
	bumpCLI    = func() string { return Version().Version }
	bumpVerify = verifyAfterBump
)

func execTool(ctx context.Context, dir, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}

func Bump(ctx context.Context, opt BumpOptions) error {
	res := &BumpResult{Check: opt.Check, CLI: bumpCLI()}
	err := bump(ctx, opt, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "bump", res, err)
	}
	return err
}

func bump(ctx context.Context, opt BumpOptions, res *BumpResult) error {
	root := opt.Dir
	if root == "" {
		root = "."
	}
	pins, floors, err := scanPins(root)
	if err != nil {
		return err
	}
	res.Floors = floors

	if opt.Check {
		res.Pins = pins
		return checkPins(root, pins, floors, res)
	}
	if opt.Version == "" {
		return UsageError(fmt.Errorf("bump needs the version to move to, `latest` included: scorix bump vX.Y.Z"))
	}

	// Without an existing require, editing go.mod would add scorix to any repo.
	if !hasKind(pins, "module") {
		return UsageError(fmt.Errorf("%s does not require %s: bump moves an app that is already on scorix", root, modulePath))
	}
	version, goVersion, err := resolveScorix(ctx, root, opt.Version)
	if err != nil {
		return err
	}
	res.Version, res.GoVersion = version, goVersion

	// Codegen follows the CLI, so the wrong one writes the previous version's files.
	if cli := res.CLI; cli != version {
		return exitErrorf(ExitFailed, "cli-mismatch",
			"this CLI is %s but the target is %s: run `scorix upgrade %s`, then bump again", cli, version, version)
	}
	// A caller ref at a tag GitHub lacks makes every CI run on the app call into nothing.
	if err := requireTag(ctx, root, version); err != nil {
		return err
	}
	if err := requireClean(ctx, root, pins); err != nil {
		return err
	}

	if err := editGoMod(ctx, root, version, goVersion, pins, res); err != nil {
		return err
	}
	if err := rewritePins(root, version, pins, res); err != nil {
		return err
	}
	fmt.Printf("==> %d pin(s) now at %s\n", len(res.Pins), version)
	for _, f := range floors {
		if semver.Compare(f.Was, version) < 0 {
			fmt.Printf("note: %s:%d says at least %s, older than %s - left alone, a floor is not a pin\n",
				f.File, f.Line, f.Was, version)
		}
	}
	if err := bumpVerify(ctx, root, res); err != nil {
		return err
	}
	res.Note = statusNote(version, pins, res)
	fmt.Printf("\n==> paste into the app's STATUS.md:\n\n%s\n", res.Note)
	return nil
}

var (
	// The two forms a pin takes: a reusable workflow ref and a `go install` of the CLI.
	pinRe = regexp.MustCompile(`(tradalab/scorix/\.github/workflows/[A-Za-z0-9._-]+\.ya?ml|github\.com/tradalab/scorix/cmd/scorix)@(v[0-9][0-9A-Za-z.\-+]*)`)
	// `must be >= v0.27.1` in a guard's own error message.
	floorRe   = regexp.MustCompile(`>=\s*(v[0-9][0-9A-Za-z.\-+]*)`)
	requireRe = regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(modulePath) + `\s+(v\S+)`)
	goDirRe   = regexp.MustCompile(`(?m)^go\s+(\S+)`)
	replaceRe = regexp.MustCompile(`(?m)^replace\s+` + regexp.QuoteMeta(modulePath) + `\s`)
)

func hasKind(pins []BumpPin, kind string) bool {
	for _, p := range pins {
		if p.Kind == kind {
			return true
		}
	}
	return false
}

// Named, not walked: a pin outside this list is one bump cannot keep honest.
func scanPins(root string) (pins, floors []BumpPin, err error) {
	gomod := filepath.Join(root, "go.mod")
	b, err := os.ReadFile(gomod)
	if err != nil {
		return nil, nil, UsageError(fmt.Errorf("no go.mod in %s: bump runs inside an app repo", root))
	}
	text := string(b)
	if m := requireRe.FindStringSubmatchIndex(text); m != nil {
		pins = append(pins, BumpPin{File: "go.mod", Line: lineOf(text, m[0]), Kind: "module", Was: text[m[4]:m[5]]})
	}
	if m := goDirRe.FindStringSubmatchIndex(text); m != nil {
		pins = append(pins, BumpPin{File: "go.mod", Line: lineOf(text, m[0]), Kind: "go", Was: text[m[2]:m[3]]})
	}
	// A leftover dev replace makes every pin a statement about a version nobody builds.
	if replaceRe.MatchString(text) {
		fmt.Printf("note: go.mod replaces %s, so the pin is not what gets built\n", modulePath)
	}

	files := []string{filepath.Join(root, "Makefile")}
	for _, pat := range []string{"*.yml", "*.yaml"} {
		found, _ := filepath.Glob(filepath.Join(root, ".github", "workflows", pat))
		files = append(files, found...)
	}
	sort.Strings(files)
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue // Makefile is optional, and a glob only returns what exists
		}
		// filepath.Rel, not a trim: with root "." a trim eats the leading dot.
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, nil, err
		}
		rel = filepath.ToSlash(rel)
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range pinRe.FindAllStringSubmatch(line, -1) {
				kind := "workflow"
				if strings.Contains(m[1], "cmd/scorix") {
					kind = "install"
				}
				pins = append(pins, BumpPin{File: rel, Line: i + 1, Kind: kind, Was: m[2]})
			}
			if strings.Contains(line, "scorix") {
				if m := floorRe.FindStringSubmatch(line); m != nil {
					floors = append(floors, BumpPin{File: rel, Line: i + 1, Kind: "floor", Was: m[1]})
				}
			}
		}
	}
	return pins, floors, nil
}

func lineOf(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}

// Offline on purpose: a guard that needs the network gets switched off.
func checkPins(root string, pins, floors []BumpPin, res *BumpResult) error {
	versions := map[string][]BumpPin{}
	for _, p := range pins {
		if p.Kind == "go" {
			continue // the Go toolchain line, not a scorix version
		}
		versions[p.Was] = append(versions[p.Was], p)
	}
	if len(versions) == 0 {
		return exitErrorf(ExitFailed, "invalid", "no scorix pin found in %s: is it an app repo?", root)
	}
	if len(versions) > 1 {
		var lines []string
		for v, ps := range versions {
			for _, p := range ps {
				lines = append(lines, fmt.Sprintf("%s:%d %s", p.File, p.Line, v))
			}
		}
		sort.Strings(lines)
		return exitErrorf(ExitFailed, "skew", "scorix is pinned to %d different versions:\n  %s",
			len(versions), strings.Join(lines, "\n  "))
	}
	var pinned string
	for v := range versions {
		pinned = v
	}
	fmt.Printf("==> every pin says %s\n", pinned)
	for _, f := range floors {
		if semver.Compare(f.Was, pinned) < 0 {
			fmt.Printf("note: %s:%d says at least %s while the app is on %s\n", f.File, f.Line, f.Was, pinned)
		}
	}
	// Only a plain release version says which build this is: the pseudo-version from
	// `scorix upgrade main` is valid semver with no build metadata.
	cli := res.CLI
	if !semver.IsValid(cli) || semver.Prerelease(cli) != "" || semver.Build(cli) != "" {
		fmt.Printf("note: this CLI is %q, not a release version - cannot compare it with the pin\n", cli)
		return nil
	}
	if cli != pinned {
		return exitErrorf(ExitFailed, "cli-mismatch",
			"the app pins %s but this CLI is %s: generated files would come from the wrong version", pinned, cli)
	}
	return nil
}

// One call answers three things: the version exists, what it resolves to, its Go line.
func resolveScorix(ctx context.Context, root, want string) (version, goVersion string, err error) {
	out, err := bumpGo(ctx, root, "go", "list", "-m", "-f", "{{.Version}} {{.GoVersion}}", modulePath+"@"+want)
	if err != nil {
		return "", "", exitErrorf(ExitFailed, "unresolved", "cannot resolve %s@%s: %s", modulePath, want, strings.TrimSpace(out))
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", "", exitErrorf(ExitFailed, "unresolved", "go list answered %q for %s@%s", strings.TrimSpace(out), modulePath, want)
	}
	return fields[0], fields[1], nil
}

func requireTag(ctx context.Context, root, version string) error {
	out, err := bumpGit(ctx, root, "git", "ls-remote", scorixRepo, "refs/tags/"+version)
	if err != nil {
		return exitErrorf(ExitMissing, "remote", "cannot read %s: %s", scorixRepo, strings.TrimSpace(out))
	}
	// Exit 0 with no ref: the remote answered and the tag is not there.
	if strings.TrimSpace(out) == "" {
		return exitErrorf(ExitFailed, "no-tag", "%s has no tag %s yet: push the tag before pointing CI at it", scorixRepo, version)
	}
	return nil
}

func requireClean(ctx context.Context, root string, pins []BumpPin) error {
	args := []string{"status", "--porcelain", "--"}
	seen := map[string]bool{}
	for _, p := range pins {
		if !seen[p.File] {
			seen[p.File] = true
			args = append(args, p.File)
		}
	}
	out, err := bumpGit(ctx, root, "git", args...)
	if err != nil {
		return nil // not a git repo, or git is absent: nothing to protect
	}
	if dirty := strings.TrimSpace(out); dirty != "" {
		return exitErrorf(ExitFailed, "dirty",
			"bump rewrites these files and they have uncommitted changes:\n%s", dirty)
	}
	return nil
}

func editGoMod(ctx context.Context, root, version, goVersion string, pins []BumpPin, res *BumpResult) error {
	if out, err := bumpGo(ctx, root, "go", "mod", "edit", "-require="+modulePath+"@"+version); err != nil {
		return exitErrorf(ExitFailed, "failed", "go mod edit -require: %s", strings.TrimSpace(out))
	}
	for _, p := range pins {
		if p.Kind == "module" {
			res.Pins = append(res.Pins, BumpPin{File: p.File, Line: p.Line, Kind: p.Kind, Was: p.Was, Now: version})
		}
	}
	// Only upwards: an app may sit on a newer Go than scorix requires.
	for _, p := range pins {
		if p.Kind != "go" || semver.Compare("v"+p.Was, "v"+goVersion) >= 0 {
			continue
		}
		if out, err := bumpGo(ctx, root, "go", "mod", "edit", "-go="+goVersion); err != nil {
			return exitErrorf(ExitFailed, "failed", "go mod edit -go: %s", strings.TrimSpace(out))
		}
		res.Pins = append(res.Pins, BumpPin{File: p.File, Line: p.Line, Kind: p.Kind, Was: p.Was, Now: goVersion})
	}
	if out, err := bumpGo(ctx, root, "go", "mod", "tidy"); err != nil {
		return exitErrorf(ExitFailed, "failed", "go mod tidy: %s", strings.TrimSpace(out))
	}
	return nil
}

func rewritePins(root, version string, pins []BumpPin, res *BumpResult) error {
	byFile := map[string][]BumpPin{}
	for _, p := range pins {
		if p.Kind == "workflow" || p.Kind == "install" {
			byFile[p.File] = append(byFile[p.File], p)
		}
	}
	for rel, want := range byFile {
		path := filepath.Join(root, filepath.FromSlash(rel))
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		next := pinRe.ReplaceAllString(string(b), "${1}@"+version)
		if next != string(b) {
			mode := os.FileMode(0o644)
			if fi, err := os.Stat(path); err == nil {
				mode = fi.Mode().Perm() // a rewrite must not change who may run the file
			}
			if err := os.WriteFile(path, []byte(next), mode); err != nil {
				return err
			}
		}
		for _, p := range want {
			res.Pins = append(res.Pins, BumpPin{File: p.File, Line: p.Line, Kind: p.Kind, Was: p.Was, Now: version})
		}
	}
	sort.Slice(res.Pins, func(i, j int) bool {
		if res.Pins[i].File != res.Pins[j].File {
			return res.Pins[i].File < res.Pins[j].File
		}
		return res.Pins[i].Line < res.Pins[j].Line
	})
	return nil
}

// The rule from RELEASE.md section C, in its order: the cross-OS build runs last
// because `lint` and `test` are what create the embed directory.
func verifyAfterBump(ctx context.Context, root string, res *BumpResult) error {
	steps := []struct {
		name string
		run  func() error
	}{
		{"generate proto", func() error { return GenerateProto(ctx, GenerateProtoOptions{Dir: root}) }},
		{"generate model", func() error { return GenerateModel(ctx, GenerateModelOptions{Dir: root}) }},
		{"validate", func() error { return Validate(ctx, ValidateOptions{Dir: root}) }},
		{"generate proto --check", func() error { return GenerateProto(ctx, GenerateProtoOptions{Dir: root, Check: true}) }},
		{"generate model --check", func() error { return GenerateModel(ctx, GenerateModelOptions{Dir: root, Check: true}) }},
		{"lint", func() error { return Lint(ctx, LintOptions{Dir: root}) }},
		{"test", func() error { return Test(ctx, TestOptions{Dir: root}) }},
		{"build windows/darwin/linux", func() error { return crossBuild(ctx, root) }},
	}
	for _, s := range steps {
		fmt.Printf("==> %s\n", s.name)
		err := s.run()
		res.Steps = append(res.Steps, BumpStep{Name: s.name, OK: err == nil, Error: errText(err)})
		if err != nil {
			return err
		}
	}
	return nil
}

func crossBuild(ctx context.Context, root string) error {
	// build.tags are part of how the app compiles, so the cross build carries them.
	args := []string{"build"}
	if _, tags, _, err := checkContext(root, nil); err == nil && len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, "./...")
	for _, goos := range []string{"windows", "darwin", "linux"} {
		c := exec.CommandContext(ctx, "go", args...)
		c.Dir = root
		c.Env = append(os.Environ(), "GOOS="+goos, "CGO_ENABLED=0")
		if out, err := c.CombinedOutput(); err != nil {
			return exitErrorf(ExitFailed, "failed", "GOOS=%s go build ./...: %s", goos, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func statusNote(version string, pins []BumpPin, res *BumpResult) string {
	files := []string{}
	seen := map[string]bool{}
	for _, p := range pins {
		if p.Kind == "workflow" || p.Kind == "install" {
			if !seen[p.File] {
				seen[p.File] = true
				files = append(files, "`"+p.File+"`")
			}
		}
	}
	sort.Strings(files)
	return fmt.Sprintf("## scorix\n\nGhim `%s` ở `go.mod`, %s - bump `<hash>` (<YYYY-MM-DD>).\n"+
		"Nghiệm: version CLI khớp · validate · generate --check 0 drift · lint · test · build ba GOOS.\n"+
		"Luật nâng: RELEASE.md §C của scorix.",
		version, strings.Join(files, ", "))
}
