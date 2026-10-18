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
	write("idl/schema.sql", schema)
	return dir
}

const manifest = "name: probe\nproto: idl/app.proto\nmodel:\n  schema: idl/schema.sql\n"

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

func TestValidateWarnsWhenMCPIsHalfWired(t *testing.T) {
	mcpProto := strings.Replace(okProto, "  rpc Ping", "  // Reports whether the app is up.\n  // @mcp\n  rpc Ping", 1)
	withMain := func(dir, body string) string {
		if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	relayMain := "package main\n\nfunc main() { _ = app.RunMCPProxy(manifest) }\n"
	cases := []struct {
		name, want string
		dir        string
	}{
		{"tools declared, switch off", "mcp.enabled is off",
			withMain(project(t, manifest, mcpProto, okSchema), relayMain)},
		{"switch on, no tool", "no rpc is @mcp",
			withMain(project(t, manifest+"mcp:\n  enabled: true\n", okProto, okSchema), relayMain)},
		{"switch on, main.go never relays", "RunMCPProxy",
			withMain(project(t, manifest+"mcp:\n  enabled: true\n", mcpProto, okSchema), "package main\n\nfunc main() {}\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := validate(t, c.dir)
			if err != nil {
				t.Fatalf("a half-wired MCP setup must warn, not fail: %v", err)
			}
			var hit bool
			for _, f := range only(t, res, "warn") {
				hit = hit || (f.Source == "mcp" && strings.Contains(f.Message, c.want))
			}
			if !hit {
				t.Fatalf("no mcp warning mentioning %q; got %+v", c.want, res.Findings)
			}
		})
	}
	ok, err := validate(t, withMain(project(t, manifest+"mcp:\n  enabled: true\n", mcpProto, okSchema), relayMain))
	if err != nil || len(only(t, ok, "warn")) != 0 {
		t.Fatalf("a fully wired MCP setup still warned: %v %+v", err, ok.Findings)
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

func TestValidateWarnsWhenCLIIsHalfWired(t *testing.T) {
	cliProto := strings.Replace(okProto, "  rpc Ping", "  // Reports whether the app is up.\n  // @cli headless\n  rpc Ping", 1)
	cases := []struct{ name, want, dir string }{
		{"commands declared, switch off", "cli.enabled is off", project(t, manifest, cliProto, okSchema)},
		{"switch on, no command", "no rpc is @cli", project(t, manifest+"cli:\n  enabled: true\n", okProto, okSchema)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := validate(t, c.dir)
			if err != nil {
				t.Fatalf("a half-wired CLI must warn, not fail: %v", err)
			}
			var hit bool
			for _, f := range only(t, res, "warn") {
				hit = hit || (f.Source == "cli" && strings.Contains(f.Message, c.want))
			}
			if !hit {
				t.Fatalf("no cli warning mentioning %q; got %+v", c.want, res.Findings)
			}
		})
	}
	wired := project(t, manifest+"cli:\n  enabled: true\n", cliProto, okSchema)
	if err := os.WriteFile(filepath.Join(wired, "main.go"), []byte("package main\n\nfunc main() { app.RunCLI(manifest, nil, setup) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok, err := validate(t, wired)
	if err != nil || len(only(t, ok, "warn")) != 0 {
		t.Fatalf("a fully wired CLI setup still warned: %v %+v", err, ok.Findings)
	}
	// The first scaffold named RunCLI in a comment, and validate stayed quiet.
	if err := os.WriteFile(filepath.Join(wired, "main.go"), []byte("package main\n\n// setup is shared with app.RunCLI.\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := validate(t, wired)
	var hit bool
	for _, f := range only(t, res, "warn") {
		hit = hit || (f.Source == "cli" && strings.Contains(f.Message, "RunCLI"))
	}
	if !hit {
		t.Fatalf("main.go without RunCLI did not warn: %+v", res.Findings)
	}
}

func TestValidateReportsWhatGenerateWouldRefuse(t *testing.T) {
	for name, proto := range map[string]string{
		"@cli without a description": strings.Replace(okProto, "  rpc Ping", "  // @cli\n  rpc Ping", 1),
		"@mcp on a stream":           strings.Replace(okProto, "  rpc Ping (PingRequest) returns (PingReply);", "  // Pings.\n  // @mcp\n  rpc Ping (PingRequest) returns (stream PingReply);", 1),
	} {
		res, _ := validate(t, project(t, manifest, proto, okSchema))
		if len(only(t, res, "error")) == 0 {
			t.Errorf("%s: validate passed what generate refuses: %+v", name, res.Findings)
		}
	}
}

func TestValidateWarnsWhenTheMSIPredatesTheCLI(t *testing.T) {
	cliProto := strings.Replace(okProto, "  rpc Ping", "  // Reports whether the app is up.\n  // @cli headless\n  rpc Ping", 1)
	dir := project(t, manifest+"cli:\n  enabled: true\n", cliProto, okSchema)
	wxs := filepath.Join(dir, "installer", "windows", "product.wxs")
	if err := os.MkdirAll(filepath.Dir(wxs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wxs, []byte("<Wix/>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() { app.RunCLI(manifest, nil, setup) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := validate(t, dir)
	var hit bool
	for _, f := range only(t, res, "warn") {
		hit = hit || (f.Source == "cli" && strings.Contains(f.Message, "product.wxs"))
	}
	if !hit {
		t.Fatalf("an old product.wxs did not warn: %+v", res.Findings)
	}
	if err := os.WriteFile(wxs, []byte("<?if $(var.HasCLI) = 1 ?><?endif?>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, _ := validate(t, dir); len(only(t, res, "warn")) != 0 {
		t.Fatalf("an up to date product.wxs still warned: %+v", res.Findings)
	}
}

func TestValidateWarnsWhenMainOutlivesTheLastCLIRpc(t *testing.T) {
	dir := project(t, manifest, okProto, okSchema)
	main := "package main\n\nfunc main() { if app.IsCLI(os.Args[1:], handler.CLIWords) { app.RunCLI(manifest, nil, setup) } }\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	res, _ := validate(t, dir)
	var hit bool
	for _, f := range only(t, res, "warn") {
		hit = hit || (f.Source == "cli" && strings.Contains(f.Message, "CLIWords"))
	}
	if !hit {
		t.Fatalf("no warning for a main.go naming CLIWords with no @cli left: %+v", res.Findings)
	}
}

func TestValidateAcceptsAnAppWithoutTables(t *testing.T) {
	dir := project(t, "name: probe\nproto: idl/app.proto\n", okProto, "")
	res, err := validate(t, dir)
	if err != nil || len(res.Findings) != 0 || res.Schema != "" {
		t.Fatalf("err %v, schema %q, findings %+v", err, res.Schema, res.Findings)
	}
	dir = project(t, manifest, okProto, "")
	if _, err := validate(t, dir); err == nil {
		t.Fatal("a declared schema that is missing passed")
	}
}
