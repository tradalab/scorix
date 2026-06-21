package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tradalab/scorix/internal/cli/runner/dialect"
)

// --- M16: all-or-nothing staging ---------------------------------------------

// TestRenderGeneratedFile_FailureLeavesNoFile: a render/format error surfaces
// before any write, so a batch caller aborts with the filesystem untouched.
func TestRenderGeneratedFile_FailureLeavesNoFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "broken.go")

	// A Go file whose template renders to non-compiling source — format.Source
	// fails, so renderGeneratedFile must return an error and never write.
	_, err := renderGeneratedFile(generatedFile{
		Path:     dst,
		Template: "package {{.Bad}} this is not go",
		Data:     struct{ Bad string }{Bad: "x"},
		Go:       true,
		Force:    true,
	})
	if err == nil {
		t.Fatal("expected render/format error, got nil")
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("expected no file written on render failure, but %s exists (stat err: %v)", dst, statErr)
	}
}

// TestRenderThenCommit_Batch_AbortsBeforeAnyWrite: in the two-pass batch used by
// GenerateProto/GenerateModel, if any render fails no file in the batch commits.
func TestRenderThenCommit_Batch_AbortsBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	bad := filepath.Join(dir, "bad.go")

	files := []generatedFile{
		{Path: good, Template: "hello {{.Name}}", Data: struct{ Name string }{"world"}, Force: true},
		{Path: bad, Template: "package main\nfunc {{.Name}}( {", Data: struct{ Name string }{"f"}, Go: true, Force: true},
	}

	// Pass 1: render all.
	var staged []stagedFile
	var renderErr error
	for _, f := range files {
		s, err := renderGeneratedFile(f)
		if err != nil {
			renderErr = err
			break
		}
		staged = append(staged, s)
	}
	if renderErr == nil {
		t.Fatal("expected a render error from the malformed Go file")
	}

	// Because render failed, the caller never reaches the commit pass. Assert
	// the would-be-good file was NOT written.
	if _, err := os.Stat(good); !os.IsNotExist(err) {
		t.Fatalf("good file must not be written when a sibling render fails: stat err %v", err)
	}
}

// TestCommitStagedFile_Skip is a no-op write.
func TestCommitStagedFile_Skip(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "skip.txt")
	if err := commitStagedFile(stagedFile{Path: dst, Action: "skipped", NeedsWrite: false}); err != nil {
		t.Fatalf("commit of skipped file should be a no-op: %v", err)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("skipped file must not be created: stat err %v", err)
	}
}

// TestRenderGeneratedFile_SkipExisting preserves the skip-when-exists-and-not-forced
// semantic, and Force overwrites.
func TestRenderGeneratedFile_SkipExisting(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "exists.txt")
	if err := os.WriteFile(dst, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := renderGeneratedFile(generatedFile{Path: dst, Template: "new", Force: false})
	if err != nil {
		t.Fatal(err)
	}
	if s.Action != "skipped" || s.NeedsWrite {
		t.Fatalf("expected skipped/no-write for existing unforced file, got %+v", s)
	}
	if err := commitStagedFile(s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if string(b) != "original" {
		t.Fatalf("skipped file must keep original content, got %q", b)
	}

	// Force → updated, content replaced.
	s, err = renderGeneratedFile(generatedFile{Path: dst, Template: "new", Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if s.Action != "updated" || !s.NeedsWrite {
		t.Fatalf("expected updated/write for forced existing file, got %+v", s)
	}
	if err := commitStagedFile(s); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(dst)
	if string(b) != "new" {
		t.Fatalf("forced file should be overwritten, got %q", b)
	}
}

// TestGenerateProto_RenderFailureWritesNothing: end-to-end guard that a failing
// render mid-batch (invalid proto → no valid Go) leaves no half-written project.
func TestGenerateProto_RenderFailureWritesNothing(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/app\n\ngo 1.26\n")
	// Empty proto → GenerateProto errors before producing files.
	mustWrite(t, filepath.Join(root, "idl", "app.proto"), "")

	err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: root})
	if err == nil {
		t.Fatal("expected error for empty proto")
	}
	// Nothing under internal/ should have been created.
	if _, statErr := os.Stat(filepath.Join(root, "internal", "types", "types.go")); !os.IsNotExist(statErr) {
		t.Fatalf("no Go output should exist after a failed generation: %v", statErr)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- M17: DEFAULT detected as a whole word -----------------------------------

func TestParseColumn_IsDefaultNotTreatedAsDefault(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tables := parseInline(t, `
CREATE TABLE thing (
    id          INTEGER PRIMARY KEY,
    is_default  INTEGER NOT NULL
);
`, d)
	col := findColumnByName(&tables[0], "is_default")
	if col == nil {
		t.Fatal("is_default column not parsed")
	}
	if col.DefaultValue != "" {
		t.Fatalf("is_default must not be detected as a DEFAULT clause, got DefaultValue=%q", col.DefaultValue)
	}
}

func TestParseColumn_RealDefaultStillDetected(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tables := parseInline(t, `
CREATE TABLE thing (
    id          INTEGER PRIMARY KEY,
    is_default  INTEGER NOT NULL DEFAULT 0,
    name        TEXT NOT NULL DEFAULT 'hello'
);
`, d)
	tbl := tables[0]

	flag := findColumnByName(&tbl, "is_default")
	if flag == nil || flag.DefaultValue != "0" {
		t.Fatalf("is_default with a real DEFAULT should capture 0, got %+v", flag)
	}
	name := findColumnByName(&tbl, "name")
	if name == nil || name.DefaultValue != "'hello'" {
		t.Fatalf("name DEFAULT literal should be captured verbatim, got %+v", name)
	}
}

func TestIndexKeyword_WholeWord(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        int
	}{
		{"IS_DEFAULT INTEGER", "DEFAULT", -1},
		{"INTEGER DEFAULT 0", "DEFAULT", 8},
		{"DEFAULT 0", "DEFAULT", 0},
		{"X DEFAULT", "DEFAULT", 2},
		{"DEFAULTS ONLY", "DEFAULT", -1},
		{"PREDEFAULT", "DEFAULT", -1},
	}
	for _, c := range cases {
		if got := indexKeyword(c.hay, c.needle); got != c.want {
			t.Errorf("indexKeyword(%q,%q) = %d, want %d", c.hay, c.needle, got, c.want)
		}
	}
}

// --- M18: paren-depth body extraction ----------------------------------------

// TestParseTable_BodyWithEmbeddedCloseParen ensures a `);` inside the body (here
// inside a DEFAULT expression and a CHECK) does not prematurely terminate the
// table body.
func TestParseTable_BodyWithEmbeddedCloseParen(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tables := parseInline(t, `
CREATE TABLE doc (
    id      TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(4)))),
    tags    TEXT NOT NULL DEFAULT (json_array()),
    status  TEXT NOT NULL CHECK (status IN ('a','b')),
    body    TEXT
);
`, d)
	if len(tables) != 1 {
		t.Fatalf("expected exactly 1 table, got %d", len(tables))
	}
	tbl := tables[0]
	// `body` is the LAST real column — it only survives if body extraction did
	// not stop at an earlier `)`.
	if findColumnByName(&tbl, "body") == nil {
		t.Fatalf("body column missing — body extraction terminated early; cols=%+v", tbl.Columns)
	}
	if c := findColumnByName(&tbl, "tags"); c == nil || c.DefaultValue != "(json_array())" {
		t.Fatalf("tags DEFAULT expression mis-bounded: %+v", c)
	}
}

// TestParseSchema_MultipleTables ensures multi-statement schemas each parse, and
// that a `);` inside one table's body does not swallow the following table.
func TestParseSchema_MultipleTables(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tables := parseInline(t, `
CREATE TABLE a (
    id     INTEGER PRIMARY KEY,
    blob   TEXT NOT NULL DEFAULT (json_object('k', 'v')),
    name   TEXT NOT NULL
);

CREATE TABLE b (
    id     INTEGER PRIMARY KEY,
    label  TEXT NOT NULL
);
`, d)
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d: %+v", len(tables), tableNames(tables))
	}
	if tables[0].Name != "a" || tables[1].Name != "b" {
		t.Fatalf("table names/order wrong: %+v", tableNames(tables))
	}
	if findColumnByName(&tables[1], "label") == nil {
		t.Fatalf("second table 'b' lost its columns: %+v", tables[1].Columns)
	}
}

// TestParseSchema_CreateTableInStringLiteralIgnored verifies the string-blanking
// path still protects the scanner: a CREATE TABLE inside a string literal must
// not be parsed as a real table.
func TestParseSchema_CreateTableInStringLiteralIgnored(t *testing.T) {
	d := dialect.MustNew("sqlite")
	tables := parseInline(t, `
CREATE TABLE real (
    id    INTEGER PRIMARY KEY,
    note  TEXT NOT NULL DEFAULT 'CREATE TABLE fake (x INTEGER);'
);
`, d)
	if len(tables) != 1 || tables[0].Name != "real" {
		t.Fatalf("CREATE TABLE inside a string literal must be ignored, got %+v", tableNames(tables))
	}
}

func tableNames(ts []sqlTable) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

// --- M19: doctor go-version parsing ------------------------------------------

func TestParseGoVersion(t *testing.T) {
	cases := []struct {
		in             string
		wantMa, wantMi int
		wantOK         bool
	}{
		{"go version go1.26.0 windows/amd64", 1, 26, true},
		{"go version go1.25.4 linux/amd64", 1, 25, true},
		{"go version go2.0.1 darwin/arm64", 2, 0, true},
		{"go version go1.26 windows/amd64", 1, 26, true},
		{"garbage output", 0, 0, false},
	}
	for _, c := range cases {
		ma, mi, ok := parseGoVersion(c.in)
		if ok != c.wantOK || (ok && (ma != c.wantMa || mi != c.wantMi)) {
			t.Errorf("parseGoVersion(%q) = (%d,%d,%v), want (%d,%d,%v)",
				c.in, ma, mi, ok, c.wantMa, c.wantMi, c.wantOK)
		}
	}
}
