package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const okProto = `syntax = "proto3";
package probe;
message Empty {}
message PingRequest {}
message PingReply { string status = 1; }
service Healthz {
  rpc Ping (PingRequest) returns (PingReply);
}
`

const okSchema = `CREATE TABLE IF NOT EXISTS users (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL
);
`

// project writes a manifest plus whatever inputs the case needs; an empty string
// means "do not create this file at all".
func project(t *testing.T, manifest, proto, schema string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		if body == "" {
			return
		}
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("scorix.yaml", manifest)
	write("idl/app.proto", proto)
	write("etc/schema.sql", schema)
	return dir
}

const manifest = "name: probe\nproto: idl/app.proto\nmodel:\n  schema: etc/schema.sql\n"

func validate(t *testing.T, dir string) (*ValidateResult, error) {
	t.Helper()
	res := &ValidateResult{}
	err := validateProject(ValidateOptions{Dir: dir}, res)
	return res, err
}

func only(t *testing.T, res *ValidateResult, severity string) []Finding {
	t.Helper()
	var out []Finding
	for _, f := range res.Findings {
		if f.Severity == severity {
			out = append(out, f)
		}
	}
	return out
}

func TestValidateAcceptsWhatInitProduces(t *testing.T) {
	res, err := validate(t, project(t, manifest, okProto, okSchema))
	if err != nil {
		t.Fatalf("a freshly scaffolded project should pass: %v (%+v)", err, res.Findings)
	}
	if res.Services != 1 || res.Tables != 1 || res.Messages != 3 {
		t.Fatalf("counted %d services, %d messages, %d tables", res.Services, res.Messages, res.Tables)
	}
}

func TestValidateReportsEachBrokenInput(t *testing.T) {
	cases := []struct {
		name     string
		dir      string
		severity string
		source   string
		contains string
	}{
		{
			name:     "manifest missing",
			dir:      project(t, "", okProto, okSchema),
			severity: "error", source: "manifest", contains: "scorix.yaml",
		},
		{
			name:     "proto file missing",
			dir:      project(t, manifest, "", okSchema),
			severity: "error", source: "proto", contains: "app.proto",
		},
		{
			name:     "message never closed",
			dir:      project(t, manifest, strings.Replace(okProto, "message PingReply { string status = 1; }", "message PingReply { string status = 1;", 1), okSchema),
			severity: "error", source: "proto", contains: "never closed",
		},
		{
			name:     "rpc names a message nobody declared",
			dir:      project(t, manifest, strings.Replace(okProto, "returns (PingReply)", "returns (Nope)", 1), okSchema),
			severity: "error", source: "proto", contains: "never declares",
		},
		{
			// The opening brace goes and the header never matches at all, so the
			// message simply is not there. The rpc type check is what catches it.
			name:     "message header broken",
			dir:      project(t, manifest, strings.Replace(okProto, "message PingReply {", "message PingReply ", 1), okSchema),
			severity: "error", source: "proto", contains: "never declares",
		},
		{
			name:     "schema file missing",
			dir:      project(t, manifest, okProto, ""),
			severity: "error", source: "schema", contains: "schema.sql",
		},
		{
			name:     "schema declares no table",
			dir:      project(t, manifest, okProto, "-- everything is commented out\n"),
			severity: "warn", source: "schema", contains: "CLEARS",
		},
		{
			name:     "proto declares no service",
			dir:      project(t, manifest, "syntax = \"proto3\";\npackage probe;\nmessage Empty {}\n", okSchema),
			severity: "warn", source: "proto", contains: "no service",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := validate(t, c.dir)
			got := only(t, res, c.severity)
			if len(got) == 0 {
				t.Fatalf("no %s finding; got %+v", c.severity, res.Findings)
			}
			f := got[0]
			if f.Source != c.source || !strings.Contains(f.Message, c.contains) {
				t.Fatalf("finding was %+v, want source %s containing %q", f, c.source, c.contains)
			}
			if c.severity == "error" && err == nil {
				t.Fatal("an error finding must fail the command, or CI stays green on it")
			}
			if c.severity == "warn" && err != nil {
				t.Fatalf("a warning must not fail the command: %v", err)
			}
		})
	}
}
