package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const breakingBefore = `syntax = "proto3";
package acme.v1;
import "scorix/v1/annotations.proto";
message Req {
  string id = 1;
  int32 count = 2;
}
message Empty {}
service Links {
  rpc GetLink (Req) returns (Empty) { option (scorix.v1.http) = { get: "/v1/links/{id}" }; }
}
`

func writeProto(t *testing.T, dir, name, src string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBreakingExitsWithItsOwnStatusAndListsOnlyTheLevelAskedFor(t *testing.T) {
	dir := t.TempDir()
	before := writeProto(t, dir, "old.proto", breakingBefore)
	after := writeProto(t, dir, "new.proto", strings.NewReplacer(`get: "/v1/links/{id}"`, `get: "/v1/link/{id}"`, "int32 count = 2;", "int64 count = 2;").Replace(breakingBefore))
	for level, want := range map[string][]string{"wire": nil, "json": {"json"}, "contract": {"json", "contract"}} {
		var out bytes.Buffer
		err := Breaking(context.Background(), BreakingOptions{Before: before, After: after, Level: level, JSONOut: &out})
		var doc struct {
			Exit int
			Data BreakingResult
		}
		if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
			t.Fatalf("%s: %v in %s", level, jerr, out.String())
		}
		var got []string
		for _, c := range doc.Data.Changes {
			got = append(got, c.Level)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("--level %s listed %v, want %v", level, got, want)
		}
		var ee *ExitError
		switch {
		case want == nil && err != nil:
			t.Errorf("--level %s with nothing at that level = %v", level, err)
		case want != nil && (!errors.As(err, &ee) || ee.Code != ExitBreaking || doc.Exit != ExitBreaking):
			t.Errorf("--level %s = %v (exit %d), want exit %d", level, err, doc.Exit, ExitBreaking)
		}
	}
}

func TestABadLevelIsAUsageError(t *testing.T) {
	var ee *ExitError
	err := Breaking(context.Background(), BreakingOptions{Before: "x", After: "y", Level: "binary"})
	if !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Fatalf("--level binary = %v", err)
	}
}

func TestTheOldSideCanBeAGitRevision(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("needs git")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q")
	writeProto(t, dir, "app.proto", breakingBefore)
	run("add", "app.proto")
	run("commit", "-qm", "x")
	after := writeProto(t, dir, "app.proto", strings.Replace(breakingBefore, "  int32 count = 2;\n", "", 1))
	t.Chdir(dir)
	err = Breaking(context.Background(), BreakingOptions{Before: "HEAD:app.proto", After: after, Level: "contract"})
	if err == nil || !strings.Contains(err.Error(), "1 breaking change") {
		t.Fatalf("against HEAD = %v", err)
	}
}
