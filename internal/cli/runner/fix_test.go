package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const legacyProto = `syntax = "proto3";
package probe;
message PingRequest {}
message PingReply { string status = 1; }
service Healthz {
  // Reports whether the app is up.
  // @cli headless
  rpc Ping (PingRequest) returns (PingReply);
}
`

func TestFixRewritesTheProjectProtoAndCheckOnlyReports(t *testing.T) {
	dir := project(t, manifest, legacyProto, okSchema)
	path := filepath.Join(dir, "idl/app.proto")
	var ee *ExitError
	if err := Fix(context.Background(), FixOptions{Dir: dir, Check: true}); !errors.As(err, &ee) || ee.Code != ExitDrift {
		t.Fatalf("--check on a proto with comments = %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != legacyProto {
		t.Fatal("--check wrote the proto")
	}
	var out bytes.Buffer
	if err := Fix(context.Background(), FixOptions{Dir: dir, JSONOut: &out}); err != nil {
		t.Fatal(err)
	}
	var doc struct{ Data FixResult }
	json.Unmarshal(out.Bytes(), &doc)
	b, _ := os.ReadFile(path)
	if !doc.Data.Changed || doc.Data.Proto != "idl/app.proto" || !strings.Contains(string(b), "option (scorix.v1.cli) = { headless: true };") {
		t.Fatalf("fix = %+v\n%s", doc.Data, b)
	}
	if err := Fix(context.Background(), FixOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("--check after fix = %v", err)
	}
}

func TestFixTakesALoneProto(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rpc.proto")
	os.WriteFile(p, []byte(legacyProto), 0o600)
	if err := Fix(context.Background(), FixOptions{Proto: p}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "(scorix.v1.cli)") {
		t.Fatalf("lone proto = %s", b)
	}
}
