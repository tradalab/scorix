package runner

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The count goes into the --json envelope, and a rewrite moves the mtime dev watches.
func TestGenerateReportsAndWritesOnlyWhatChanged(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour)
	watched := []string{
		filepath.Join(dir, "internal", "handler", "handler.go"),
		filepath.Join(dir, "internal", "model", "users_model_gen.go"),
	}
	for _, p := range watched {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct {
		name string
		run  func(out *bytes.Buffer) error
	}{
		{"proto", func(out *bytes.Buffer) error {
			return GenerateProto(ctx, GenerateProtoOptions{Dir: dir, JSONOut: out})
		}},
		{"model", func(out *bytes.Buffer) error {
			return GenerateModel(ctx, GenerateModelOptions{Dir: dir, JSONOut: out})
		}},
	} {
		var buf bytes.Buffer
		if err := tc.run(&buf); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		data := decodeResult(t, &buf)["data"].(map[string]any)
		if n, _ := data["updated"].(float64); n != 0 {
			t.Errorf("%s: a rerun with unchanged inputs reported updated=%v", tc.name, n)
		}
		if n, _ := data["unchanged"].(float64); n == 0 {
			t.Errorf("%s: nothing was reported as unchanged: %v", tc.name, data)
		}
	}

	for _, p := range watched {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !fi.ModTime().Equal(old) {
			t.Errorf("%s was rewritten although its content did not change", filepath.Base(p))
		}
	}
}

// Hand-edited around its marker zones: a pointless rewrite still moves the file.
func TestGenerateModelLeavesTheServiceContextAloneWhenNothingChanges(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	svc := filepath.Join(dir, "internal", "svc", "service_context.go")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(svc, old, old); err != nil {
		t.Fatal(err)
	}

	said := captureStdout(t, func() {
		if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir}); err != nil {
			t.Fatal(err)
		}
	})
	if fi, err := os.Stat(svc); err != nil {
		t.Fatal(err)
	} else if !fi.ModTime().Equal(old) {
		t.Error("service_context.go was rewritten although the wiring did not change")
	}
	if strings.Contains(said, "Patched") {
		t.Errorf("it reported a patch it did not make:\n%s", said)
	}
}

// Two totals for one project means one of them is wrong, and nothing says which.
func TestGenerateModelCountsTheSameFilesWhetherItWritesOrChecks(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	count := func(check bool) float64 {
		var buf bytes.Buffer
		if err := GenerateModel(ctx, GenerateModelOptions{Dir: dir, Check: check, JSONOut: &buf}); err != nil {
			t.Fatal(err)
		}
		n, _ := decodeResult(t, &buf)["data"].(map[string]any)["files"].(float64)
		return n
	}
	if w, c := count(false), count(true); w != c {
		t.Errorf("files = %v when writing and %v when checking", w, c)
	}
}

// The icon is the one binary here, and the file an app replaces with its own art.
func TestScaffoldingKeepsAnIconTheAppAlreadyHas(t *testing.T) {
	dir := t.TempDir()
	data := map[string]string{"Name": "demo", "Package": "demo", "Shell": "vanilla-ts"}
	if err := writeTemplateFS("static/project", dir, data); err != nil {
		t.Fatal(err)
	}
	icon := filepath.Join(dir, "assets", "icon.ico")
	mine := []byte("the app's own icon")
	if err := os.WriteFile(icon, mine, 0o644); err != nil {
		t.Fatal(err)
	}

	said := captureStdout(t, func() {
		if err := writeTemplateFS("static/project", dir, data); err != nil {
			t.Fatal(err)
		}
	})
	got, err := os.ReadFile(icon)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, mine) {
		t.Error("scaffolding overwrote the icon the app had already put there")
	}
	if strings.Contains(said, "created: "+icon) {
		t.Errorf("it reported creating a file it did not write:\n%s", said)
	}
}

func captureStdout(t *testing.T, run func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	real := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	run()
	os.Stdout = real
	_ = w.Close()
	return <-done
}
