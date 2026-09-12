package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const checkManifest = "name: fixture\n"

const checkGoMod = `module fixture

go 1.27.0
`

// checkFixture writes a throwaway module: no dependencies, so `go test` and
// `go vet` run offline and in about a second.
func checkFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	all := map[string]string{"scorix.yaml": checkManifest, "go.mod": checkGoMod}
	for k, v := range files {
		all[k] = v
	}
	for rel, body := range all {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestTestReportsTheFailingTestNotJustAnExitCode(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"fixture_test.go": `package fixture

import "testing"

func TestPasses(t *testing.T) {}

func TestFails(t *testing.T) { t.Fatal("expected 42, got 41") }
`})

	res := &TestResult{}
	err := runTests(context.Background(), TestOptions{Dir: dir}, res)
	if err == nil {
		t.Fatal("a failing test must fail the command, or CI stays green on it")
	}
	if res.Passed != 1 || res.Failed != 1 {
		t.Fatalf("counted passed=%d failed=%d, want 1 and 1", res.Passed, res.Failed)
	}
	if len(res.Failures) == 0 {
		t.Fatal("no failures[]: the caller is left with an exit code and nothing to act on")
	}
	var found bool
	for _, f := range res.Failures {
		if f.Test == "TestFails" && strings.Contains(f.Output, "expected 42, got 41") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failing test's own message is missing: %+v", res.Failures)
	}
}

// A package that does not compile has no tests to report, so the answer has to
// arrive in the same diagnostics[] shape build uses.
func TestTestReportsACompileErrorAsDiagnostics(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"broken.go": "package fixture\n\nfunc Broken() int { return \"not an int\" }\n",
	})

	res := &TestResult{}
	if err := runTests(context.Background(), TestOptions{Dir: dir}, res); err == nil {
		t.Fatal("a package that does not compile must fail")
	}
	if len(res.Diagnostics) == 0 {
		t.Fatalf("no diagnostics[]: %+v", res)
	}
	d := res.Diagnostics[0]
	if !strings.HasSuffix(d.File, "broken.go") || d.Line == 0 || d.Message == "" {
		t.Fatalf("diagnostic does not point anywhere: %+v", d)
	}
}

func TestLintReportsFindingsWithAPosition(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"vetme.go": "package fixture\n\nimport \"fmt\"\n\nfunc Bad() { fmt.Printf(\"%d\", \"a string\") }\n",
	})

	res := &LintResult{}
	if err := runLint(context.Background(), LintOptions{Dir: dir}, res); err == nil {
		t.Fatal("a vet finding must fail the command")
	}
	if len(res.Findings) == 0 {
		t.Fatalf("no findings[]: %+v", res)
	}
	f := res.Findings[0]
	if !strings.HasSuffix(f.File, "vetme.go") || f.Line == 0 || f.Message == "" {
		t.Fatalf("finding does not point anywhere: %+v", f)
	}
}

func TestCleanProjectPassesBothChecks(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"ok.go":      "package fixture\n\nfunc Add(a, b int) int { return a + b }\n",
		"ok_test.go": "package fixture\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"math\")\n\t}\n}\n",
	})

	tres := &TestResult{}
	if err := runTests(context.Background(), TestOptions{Dir: dir}, tres); err != nil {
		t.Fatalf("a clean project failed: %v (%+v)", err, tres)
	}
	if tres.Passed != 1 {
		t.Fatalf("passed=%d, want 1", tres.Passed)
	}
	lres := &LintResult{}
	if err := runLint(context.Background(), LintOptions{Dir: dir}, lres); err != nil {
		t.Fatalf("a clean project failed vet: %v (%+v)", err, lres)
	}
}

// The embed directory is created before Go runs, because //go:embed all:.scorix/dist
// has to compile before a test can say anything at all.
func TestChecksCreateTheEmbedDirectory(t *testing.T) {
	dir := checkFixture(t, map[string]string{"ok.go": "package fixture\n"})
	if _, _, _, err := checkContext(dir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".scorix", "dist")); err != nil {
		t.Fatalf("embed dir missing: %v", err)
	}
}

// A panicking test aborts the binary, so the runner may never emit a `fail`
// action for it. Vanishing is the worst outcome: the count reads as a pass.
func TestPanickingTestIsStillReported(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"panic_test.go": `package fixture

import "testing"

func TestBoom(t *testing.T) { panic("kaboom") }
`})

	res := &TestResult{}
	err := runTests(context.Background(), TestOptions{Dir: dir}, res)
	if err == nil {
		t.Fatalf("a panicking test reported success: %+v", res)
	}
	if res.Failed == 0 && len(res.Failures) == 0 {
		t.Fatalf("the panic left no trace in the result: %+v", res)
	}
	var mentions bool
	for _, f := range res.Failures {
		if strings.Contains(f.Output, "kaboom") {
			mentions = true
		}
	}
	if !mentions {
		t.Fatalf("the panic message is missing, so there is nothing to act on: %+v", res.Failures)
	}
}

// "exit status 1" is not an answer. Compile errors arrive on the JSON stream,
// but a toolchain that fails before that - a broken go.mod, an unresolvable
// module - reports only on stderr, and dropping it leaves an agent with an exit
// code and nothing to act on.
func TestToolchainFailureCarriesItsReason(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"go.mod": "module fixture\ngo BROKEN\n",
		"ok.go":  "package fixture\n",
	})

	cases := []struct {
		name string
		run  func() error
	}{
		{"test", func() error { return runTests(context.Background(), TestOptions{Dir: dir}, &TestResult{}) }},
		{"lint", func() error { return runLint(context.Background(), LintOptions{Dir: dir}, &LintResult{}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.run()
			if err == nil {
				t.Fatal("a broken go.mod reported success")
			}
			if !strings.Contains(err.Error(), "go.mod") {
				t.Fatalf("the reason is missing, only %q survived", err)
			}
		})
	}
}

// A failing package emits its own fail event on top of each failing test's, and
// that one says nothing but "FAIL <pkg>" - so len(failures) stopped matching
// failed, which is the number an agent reports back.
func TestPackageLevelFailureDoesNotDoubleCount(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"dup_test.go": `package fixture

import "testing"

func TestOne(t *testing.T)  {}
func TestBoom(t *testing.T) { t.Fatal("boom") }
`})

	res := &TestResult{}
	if err := runTests(context.Background(), TestOptions{Dir: dir}, res); err == nil {
		t.Fatal("expected failure")
	}
	if len(res.Failures) != res.Failed {
		t.Fatalf("failed=%d but failures[] has %d entries: %+v", res.Failed, len(res.Failures), res.Failures)
	}
	if res.Failures[0].Test != "TestBoom" {
		t.Fatalf("wrong entry kept: %+v", res.Failures[0])
	}
}

// The package-level entry is not noise when it is the only one: a package that
// does not compile has no test events at all.
func TestPackageLevelFailureIsKeptWhenItIsTheOnlySignal(t *testing.T) {
	dir := checkFixture(t, map[string]string{
		"broken.go": "package fixture\n\nfunc Broken() int { return \"nope\" }\n",
	})

	res := &TestResult{}
	if err := runTests(context.Background(), TestOptions{Dir: dir}, res); err == nil {
		t.Fatal("expected failure")
	}
	if len(res.Failures) == 0 && len(res.Diagnostics) == 0 {
		t.Fatalf("a package that does not compile left no trace: %+v", res)
	}
}
