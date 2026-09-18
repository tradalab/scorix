package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Shaped like the six real repos, guard message with a floor included.
func bumpApp(t *testing.T, pin, goLine string) string {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/app\n\ngo "+goLine+"\n\nrequire (\n\tgithub.com/tradalab/scorix "+pin+"\n)\n")
	write("Makefile", "# Install scorix CLI first:\n#   go install github.com/tradalab/scorix/cmd/scorix@"+pin+"\ngen:\n"+
		"\t@echo \"    Check the scorix version: it must be >= v0.27.1, then rerun make gen.\"\n")
	write(".github/workflows/pr.yml", "jobs:\n  check:\n    uses: tradalab/scorix/.github/workflows/scorix-pr.yml@"+pin+"\n")
	write(".github/workflows/release.yml", "jobs:\n  release:\n    uses: tradalab/scorix/.github/workflows/scorix-release.yml@"+pin+"\n"+
		"      - run: go install github.com/tradalab/scorix/cmd/scorix@"+pin+"\n")
	return root
}

type toolCall struct {
	name string
	args []string
}

// fakeTools stands in for the module proxy, the git remote and the generators.
func fakeTools(t *testing.T, cli, listAnswer, lsRemote, status string) *[]toolCall {
	t.Helper()
	calls := &[]toolCall{}
	oldGo, oldGit, oldCLI, oldVerify := bumpGo, bumpGit, bumpCLI, bumpVerify
	bumpGo = func(_ context.Context, _, name string, args ...string) (string, error) {
		*calls = append(*calls, toolCall{name, args})
		if len(args) > 1 && args[0] == "list" {
			return listAnswer, nil
		}
		return "", nil
	}
	bumpGit = func(_ context.Context, _, name string, args ...string) (string, error) {
		*calls = append(*calls, toolCall{name, args})
		if len(args) > 0 && args[0] == "ls-remote" {
			return lsRemote, nil
		}
		return status, nil
	}
	bumpCLI = func() string { return cli }
	bumpVerify = func(context.Context, string, *BumpResult) error { return nil }
	t.Cleanup(func() { bumpGo, bumpGit, bumpCLI, bumpVerify = oldGo, oldGit, oldCLI, oldVerify })
	return calls
}

func sawArg(calls *[]toolCall, want string) bool {
	for _, c := range *calls {
		for _, a := range c.args {
			if a == want {
				return true
			}
		}
	}
	return false
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBumpCheckAcceptsPinsThatAgree(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.28.0", "", "", "")
	if err := Bump(context.Background(), BumpOptions{Dir: root, Check: true}); err != nil {
		t.Fatalf("pins all say v0.28.0 and --check failed: %v", err)
	}
}

func TestBumpCheckCatchesOnePinLeftBehind(t *testing.T) {
	// The shape of the real failure: one caller ref forgotten, the app still builds.
	root := bumpApp(t, "v0.28.0", "1.27.0")
	pr := filepath.Join(root, ".github", "workflows", "pr.yml")
	b, _ := os.ReadFile(pr)
	if err := os.WriteFile(pr, []byte(strings.Replace(string(b), "v0.28.0", "v0.27.1", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	// A CLI without a release version, so only the skew between pins can fail here.
	fakeTools(t, "v0.28.1-0.20260916025525-708e7c8e3bc3", "", "", "")
	err := Bump(context.Background(), BumpOptions{Dir: root, Check: true})
	if err == nil || !strings.Contains(err.Error(), "2 different versions") {
		t.Fatalf("skew between pins = %v", err)
	}
	if !strings.Contains(err.Error(), ".github/workflows/pr.yml:3") {
		t.Errorf("the error does not point at the pin left behind: %v", err)
	}
}

func TestBumpCheckCatchesACLIBehindThePin(t *testing.T) {
	// The pin grep cannot find: a CLI a version behind rewrote loom's wiring in silence.
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.27.1", "", "", "")
	err := Bump(context.Background(), BumpOptions{Dir: root, Check: true})
	if err == nil || !strings.Contains(err.Error(), "this CLI is v0.27.1") {
		t.Fatalf("CLI behind the pin = %v", err)
	}
}

func TestBumpCheckCannotCompareACLIBuiltFromACheckout(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	// A bare pseudo-version is valid semver with no build metadata: `upgrade main` installs one.
	for _, cli := range []string{"v0.28.1-0.20260916025525-708e7c8e3bc3", "v0.28.1-0.20260916025525-708e7c8e3bc3+dirty", "(devel)", ""} {
		fakeTools(t, cli, "", "", "")
		if err := Bump(context.Background(), BumpOptions{Dir: root, Check: true}); err != nil {
			t.Errorf("CLI %q made --check fail: %v", cli, err)
		}
	}
}

// In CI, `bump vX --check` reads as "is this app on vX".
func TestBumpCheckAnswersAboutTheVersionItWasAsked(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")

	fakeTools(t, "v0.28.0", "", "", "")
	if err := Bump(context.Background(), BumpOptions{Dir: root, Version: "v0.28.0", Check: true}); err != nil {
		t.Fatalf("the app is on v0.28.0 and --check v0.28.0 failed: %v", err)
	}

	fakeTools(t, "v0.28.0", "", "", "")
	err := Bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0", Check: true})
	if code := exitCode(err); code != ExitDrift {
		t.Fatalf("--check v0.29.0 on an app pinned to v0.28.0 exited %d, want Drift: %v", code, err)
	}
	if !strings.Contains(err.Error(), "v0.29.0") || !strings.Contains(err.Error(), "v0.28.0") {
		t.Errorf("the error names neither side: %v", err)
	}

	// A typo is Usage, not Drift: CI branches on which.
	fakeTools(t, "v0.28.0", "", "", "")
	err = Bump(context.Background(), BumpOptions{Dir: root, Version: "not-a-version", Check: true})
	if code := exitCode(err); code != ExitUsage {
		t.Errorf("--check with an argument that is not a version exited %d, want Usage: %v", code, err)
	}

	fakeTools(t, "v0.28.0", "", "", "")
	err = Bump(context.Background(), BumpOptions{Dir: root, Version: "latest", Check: true})
	if code := exitCode(err); code != ExitUsage {
		t.Errorf("--check latest exited %d, want Usage: %v", code, err)
	}
	// "not a version" would be wrong: bump itself accepts latest.
	if err != nil && !strings.Contains(err.Error(), "network") {
		t.Errorf("--check latest blames the argument instead of the missing network: %v", err)
	}
}

func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitFailed
}

func TestBumpRewritesEveryPinAndLeavesTheFloorAlone(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	calls := fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc123\trefs/tags/v0.29.0\n", "")
	res := &BumpResult{CLI: "v0.29.0"}
	if err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, res); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".github/workflows/pr.yml", ".github/workflows/release.yml", "Makefile"} {
		body := read(t, root, rel)
		if strings.Contains(body, "v0.28.0") || !strings.Contains(body, "v0.29.0") {
			t.Errorf("%s still on the old pin: %s", rel, body)
		}
	}
	if !strings.Contains(read(t, root, "Makefile"), ">= v0.27.1") {
		t.Error("the floor in the guard message was rewritten; a floor is not a pin")
	}
	if len(res.Floors) != 1 || res.Floors[0].Was != "v0.27.1" {
		t.Errorf("floors = %+v, want the guard's v0.27.1 reported", res.Floors)
	}
	if !sawArg(calls, "-require=github.com/tradalab/scorix@v0.29.0") {
		t.Errorf("go.mod was not asked to move: %+v", *calls)
	}
	if sawArg(calls, "-go=1.27.0") {
		t.Error("the go directive was rewritten to the value it already had")
	}
	var got []string
	for _, p := range res.Pins {
		got = append(got, p.File+":"+p.Kind)
	}
	want := ".github/workflows/pr.yml:workflow .github/workflows/release.yml:workflow " +
		".github/workflows/release.yml:install Makefile:install go.mod:module"
	if strings.Join(got, " ") != want {
		t.Errorf("pins recorded = %v\nwant                    = %v", got, strings.Fields(want))
	}
}

// The default is -d ".", which is how anyone running it inside the app repo calls it.
func TestBumpWorksFromInsideTheRepo(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	t.Chdir(root)
	res := &BumpResult{CLI: "v0.29.0"}
	if err := bump(context.Background(), BumpOptions{Dir: ".", Version: "v0.29.0"}, res); err != nil {
		t.Fatal(err)
	}
	if body := read(t, root, ".github/workflows/pr.yml"); !strings.Contains(body, "v0.29.0") {
		t.Errorf("the caller was not rewritten: %s", body)
	}
	for _, p := range res.Pins {
		if strings.HasPrefix(p.File, "github/") || strings.HasPrefix(p.File, "/") {
			t.Errorf("file name lost its leading dot: %q", p.File)
		}
	}
}

func TestBumpRaisesTheGoDirectiveOnlyUpwards(t *testing.T) {
	behind := bumpApp(t, "v0.28.0", "1.26.0")
	calls := fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	if err := bump(context.Background(), BumpOptions{Dir: behind, Version: "v0.29.0"}, &BumpResult{CLI: "v0.29.0"}); err != nil {
		t.Fatal(err)
	}
	if !sawArg(calls, "-go=1.27.0") {
		t.Errorf("an app on Go 1.26.0 was not raised: %+v", *calls)
	}

	ahead := bumpApp(t, "v0.28.0", "1.28.0")
	calls = fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	if err := bump(context.Background(), BumpOptions{Dir: ahead, Version: "v0.29.0"}, &BumpResult{CLI: "v0.29.0"}); err != nil {
		t.Fatal(err)
	}
	if sawArg(calls, "-go=1.27.0") {
		t.Error("an app already on a newer Go was dragged back down")
	}
}

func TestBumpRefusesWhenThisCLIIsNotTheTarget(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.28.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, &BumpResult{CLI: "v0.28.0"})
	if err == nil || !strings.Contains(err.Error(), "scorix upgrade v0.29.0") {
		t.Fatalf("err = %v, want the upgrade command to run first", err)
	}
	if strings.Contains(read(t, root, ".github/workflows/pr.yml"), "v0.29.0") {
		t.Error("it rewrote a pin before refusing, so the repo now points at a CLI nobody has")
	}
}

func TestBumpRefusesATagTheRemoteDoesNotHave(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "", "")
	err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, &BumpResult{CLI: "v0.29.0"})
	if err == nil || !strings.Contains(err.Error(), "no tag v0.29.0") {
		t.Fatalf("err = %v, want a refusal naming the missing tag", err)
	}
	if strings.Contains(read(t, root, ".github/workflows/release.yml"), "v0.29.0") {
		t.Error("CI now points at a tag that does not exist")
	}
}

func TestBumpRefusesWhenAFileItWouldRewriteIsDirty(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", " M .github/workflows/pr.yml\n")
	err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, &BumpResult{CLI: "v0.29.0"})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("err = %v, want a refusal that names the dirty file", err)
	}
}

func TestBumpNoteNamesTheCallersItTouched(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	res := &BumpResult{CLI: "v0.29.0"}
	if err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, res); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## scorix", "`v0.29.0`", "pr.yml", "release.yml", "Makefile"} {
		if !strings.Contains(res.Note, want) {
			t.Errorf("the note is missing %q:\n%s", want, res.Note)
		}
	}
}

// `scorix init` writes a one-line require; the six apps have the block form.
func TestBumpReadsEveryGoModForm(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	gomod := filepath.Join(root, "go.mod")
	if err := os.WriteFile(gomod, []byte("module example.com/app\n\ngo 1.27.0\n\n"+
		"require github.com/tradalab/scorix v0.28.0\n\n"+
		"replace (\n\tgithub.com/tradalab/scorix => ../scorix\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	res := &BumpResult{CLI: "v0.29.0"}
	said := captureStdout(t, func() {
		if err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, res); err != nil {
			t.Fatalf("a one-line require was refused: %v", err)
		}
	})
	if !sawArg(calls, "-require=github.com/tradalab/scorix@v0.29.0") {
		t.Errorf("the module pin was not moved: %+v", *calls)
	}
	if !strings.Contains(said, "replaces github.com/tradalab/scorix") {
		t.Errorf("a replace block went unreported:\n%s", said)
	}
}

func TestBumpRefusesARepoThatDoesNotRequireScorix(t *testing.T) {
	// Otherwise `go mod edit -require` would add scorix to whatever repo this ran in.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/other\n\ngo 1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls := fakeTools(t, "v0.29.0", "v0.29.0 1.27.0\n", "abc\trefs/tags/v0.29.0\n", "")
	err := bump(context.Background(), BumpOptions{Dir: root, Version: "v0.29.0"}, &BumpResult{CLI: "v0.29.0"})
	if err == nil || !strings.Contains(err.Error(), "does not require") {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if sawArg(calls, "-require=github.com/tradalab/scorix@v0.29.0") {
		t.Error("it added scorix to a repo that never used it")
	}
}

func TestBumpWithoutAVersionIsAUsageError(t *testing.T) {
	root := bumpApp(t, "v0.28.0", "1.27.0")
	fakeTools(t, "v0.28.0", "", "", "")
	err := Bump(context.Background(), BumpOptions{Dir: root})
	if code := exitCode(err); code != ExitUsage {
		t.Fatalf("err = %v (exit %d), want a usage error", err, code)
	}
}
