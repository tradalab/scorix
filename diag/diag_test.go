package diag

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradalab/scorix/logger"
)

func read(t *testing.T, path string) Report {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rep Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatalf("report is not JSON: %v\n%s", err, b)
	}
	return rep
}

func TestPanicWritesReportWithTheLogThatLedToIt(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{Dir: dir, App: "demo", Version: "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disarm)
	logger.Info("about to do the thing")

	func() {
		defer Recover("test site")
		panic("boom")
	}()

	reports, err := List(dir)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %v, err = %v", reports, err)
	}
	rep := read(t, reports[0])
	if rep.Kind != "panic" || rep.Panic != "boom" || rep.Where != "test site" {
		t.Fatalf("report = %+v", rep)
	}
	if rep.App != "demo" || rep.Version != "1.2.3" || rep.OS == "" || rep.Go == "" {
		t.Fatalf("metadata missing: %+v", rep)
	}
	if !strings.Contains(rep.Stack, "diag.") {
		t.Fatalf("stack does not look like a stack: %q", rep.Stack)
	}
	// The ring is the whole reason this beats a bare traceback.
	if !rep.LogAvailable || !strings.Contains(strings.Join(rep.Log, "\n"), "about to do the thing") {
		t.Fatalf("ring log missing from report: %+v", rep.Log)
	}
}

func TestPruneKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{Dir: dir, App: "demo", Keep: 3}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disarm)
	for i := 0; i < 6; i++ {
		Panic("loop", i, []byte("stack"))
	}
	reports, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Fatalf("a crash loop must not fill the disk: %d reports", len(reports))
	}
}

func TestNotInitializedIsSilent(t *testing.T) {
	mu.Lock()
	on = false
	mu.Unlock()
	Panic("nowhere", "boom", []byte("stack")) // must not panic or write anything
}

// The one that matters: a panic nobody recovers kills the process, and the only
// way to see it afterwards is the file the runtime was told to write.
func TestFatalPanicIsPromotedOnTheNextStart(t *testing.T) {
	if os.Getenv("DIAG_CRASH_CHILD") == "1" {
		if err := Init(Options{Dir: os.Getenv("DIAG_CRASH_DIR"), App: "demo"}); err != nil {
			os.Exit(3)
		}
		panic("unrecovered boom")
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestFatalPanicIsPromotedOnTheNextStart")
	cmd.Env = append(os.Environ(), "DIAG_CRASH_CHILD=1", "DIAG_CRASH_DIR="+dir)
	if err := cmd.Run(); err == nil {
		t.Fatal("the child was supposed to die")
	}

	if _, err := os.Stat(filepath.Join(dir, pendingName)); err != nil {
		t.Fatalf("the runtime wrote no crash output: %v", err)
	}
	// Second start, same directory: the pending traceback becomes a report.
	if err := Init(Options{Dir: dir, App: "demo"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disarm)
	reports, err := List(dir)
	if err != nil || len(reports) != 1 {
		t.Fatalf("reports = %v, err = %v", reports, err)
	}
	rep := read(t, reports[0])
	if rep.Kind != "fatal" {
		t.Fatalf("kind = %q, want fatal", rep.Kind)
	}
	if !strings.Contains(rep.Stack, "unrecovered boom") {
		t.Fatalf("the panic value is missing from the traceback: %q", rep.Stack)
	}
	// An empty log list here must not be readable as "nothing was logged".
	if rep.LogAvailable {
		t.Fatal("a fatal crash cannot have carried a live ring log")
	}
}

// Reports must come back newest first even when several land in the same
// millisecond: prune deletes from the tail of that order.
func TestListOrdersNewestFirstWithinOneMillisecond(t *testing.T) {
	dir := t.TempDir()
	if err := Init(Options{Dir: dir, App: "demo", Keep: 100}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(disarm)
	for i := 0; i < 12; i++ {
		Panic("burst", i, []byte("stack"))
	}
	reports, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 12 {
		t.Fatalf("a burst lost reports to a name collision: %d of 12", len(reports))
	}
	newest := read(t, reports[0])
	oldest := read(t, reports[len(reports)-1])
	if newest.Panic != "11" || oldest.Panic != "0" {
		t.Fatalf("order is wrong: newest=%q oldest=%q", newest.Panic, oldest.Panic)
	}
}
