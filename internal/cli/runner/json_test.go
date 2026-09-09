package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func decodeResult(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, buf.String())
	}
	return got
}

func TestDoctorJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Doctor(context.Background(), DoctorOptions{JSONOut: &buf}); err != nil {
		t.Fatalf("doctor on a machine with Go must pass: %v", err)
	}
	got := decodeResult(t, &buf)
	if got["command"] != "doctor" || got["ok"] != true || got["exit"] != float64(ExitOK) {
		t.Fatalf("envelope = %v", got)
	}
	data := got["data"].(map[string]any)
	checks := data["checks"].([]any)
	if len(checks) == 0 {
		t.Fatal("no checks reported")
	}
	// go is the one hard requirement; the test binary could not have compiled without it.
	first := checks[0].(map[string]any)
	if first["name"] != "go" || first["status"] != "ok" {
		t.Fatalf("first check = %v", first)
	}
}

func TestGenerateProtoJSON_DriftCarriesPathAndExitCode(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()

	var buf bytes.Buffer
	err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true, JSONOut: &buf})
	if err == nil {
		t.Fatal("a never-generated project must report drift")
	}
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitDrift {
		t.Fatalf("drift must exit %d, got %v", ExitDrift, err)
	}

	got := decodeResult(t, &buf)
	if got["ok"] != false || got["exit"] != float64(ExitDrift) || got["kind"] != "drift" {
		t.Fatalf("envelope = %v", got)
	}
	data := got["data"].(map[string]any)
	drift := data["drift"].([]any)
	if len(drift) == 0 {
		t.Fatal("drift list is empty while the command reported drift")
	}
	// Structured, not a sentence: an agent must not have to parse "path (reason)".
	first := drift[0].(map[string]any)
	if first["path"] == "" || first["reason"] == "" {
		t.Fatalf("drift entry = %v", first)
	}
	if data["regen"] != "scorix generate proto" {
		t.Fatalf("regen command missing: %v", data)
	}
}

func TestGenerateProtoJSON_InSync(t *testing.T) {
	dir := newCheckProject(t)
	ctx := context.Background()
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := GenerateProto(ctx, GenerateProtoOptions{Dir: dir, Check: true, JSONOut: &buf}); err != nil {
		t.Fatalf("freshly generated project must be in sync: %v", err)
	}
	got := decodeResult(t, &buf)
	if got["ok"] != true || got["exit"] != float64(ExitOK) {
		t.Fatalf("envelope = %v", got)
	}
	data := got["data"].(map[string]any)
	if _, has := data["drift"]; has {
		t.Fatalf("a passing check must not carry a drift list: %v", data)
	}
	if data["files"].(float64) == 0 {
		t.Fatalf("a passing check must still say how many files it checked: %v", data)
	}
}

func TestBuildJSON_FailureKeepsTheEnvelope(t *testing.T) {
	var buf bytes.Buffer
	err := Build(context.Background(), BuildOptions{Dir: filepath.Join(t.TempDir(), "absent"), JSONOut: &buf})
	if err == nil {
		t.Fatal("build without scorix.yaml must fail")
	}
	got := decodeResult(t, &buf)
	if got["ok"] != false || got["exit"] != float64(ExitFailed) || got["kind"] != "failed" {
		t.Fatalf("envelope = %v", got)
	}
	if got["error"] == "" {
		t.Fatal("a failure must carry its message in the document")
	}
}

func TestGenerateModelJSON_Drift(t *testing.T) {
	dir := newCheckProject(t)
	// The model generator needs the proto pass first (service context, module path).
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err := GenerateModel(context.Background(), GenerateModelOptions{
		Dir: dir, Schema: "etc/schema.sql", Check: true, JSONOut: &buf,
	})
	if err == nil {
		t.Fatal("a never-generated model must report drift")
	}
	got := decodeResult(t, &buf)
	if got["kind"] != "drift" || got["exit"] != float64(ExitDrift) {
		t.Fatalf("envelope = %v", got)
	}
	data := got["data"].(map[string]any)
	if data["files"].(float64) == 0 {
		t.Fatalf("check mode must report how many files it looked at: %v", data)
	}
}

// The JSON document is the whole contract of --json: it has to be valid even
// when the runner printed nothing else, so nothing may be written to the same
// writer before it.
func TestEmitJSONWritesExactlyOneDocument(t *testing.T) {
	var buf bytes.Buffer
	err := EmitJSON(&buf, "probe", map[string]string{"k": "v"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(&buf)
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&map[string]any{}); err == nil {
		t.Fatal("a second document followed the first")
	}
	_ = os.Stdout // the writer is passed in, never taken from the global
}

// Exit 4 is the one status a caller acts on differently: nothing ran, so
// retrying is pointless until the tool is installed.
func TestDoctorJSON_MissingGoIsExitMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // a directory with no go in it
	var buf bytes.Buffer
	err := Doctor(context.Background(), DoctorOptions{JSONOut: &buf})
	if err == nil {
		t.Fatal("doctor without a Go toolchain must fail")
	}
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != ExitMissing || ee.Kind != "missing_prerequisite" {
		t.Fatalf("want exit %d/missing_prerequisite, got %#v", ExitMissing, err)
	}
	got := decodeResult(t, &buf)
	if got["exit"] != float64(ExitMissing) || got["kind"] != "missing_prerequisite" {
		t.Fatalf("envelope = %v", got)
	}
}
