package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tradalab/scorix/internal/cli/mcp"
)

const fixtureManifest = `name: probe
proto: idl/app.proto
model:
  schema: etc/schema.sql
`

const fixtureGoMod = `module probe

go 1.27.0
`

const fixtureProto = `syntax = "proto3";
package probe;
message Empty {}
message PingRequest {}
message PingReply { string status = 1; }
service Healthz {
  rpc Ping (PingRequest) returns (PingReply);
}
`

const fixtureSchema = `CREATE TABLE IF NOT EXISTS users (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT NOT NULL
);
`

func fixtureProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"scorix.yaml":    fixtureManifest,
		"go.mod":         fixtureGoMod,
		"idl/app.proto":  fixtureProto,
		"etc/schema.sql": fixtureSchema,
	} {
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

// callTool drives a real mcp.Server over the same transport a client uses, so the
// comparison covers argument decoding and the envelope, not just the runner call.
func callTool(t *testing.T, name string, args map[string]any) string {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := mcp.NewServer("test").Serve(context.Background(), strings.NewReader(string(req)+"\n"), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("%s answered a protocol error: %s", name, resp.Error.Message)
	}
	if len(resp.Result.Content) != 1 {
		t.Fatalf("%s returned %d content blocks", name, len(resp.Result.Content))
	}
	return resp.Result.Content[0].Text
}

func canonical(t *testing.T, doc string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, doc)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The MCP layer restates every option the cobra flags declare, and the defaults
// live only in those flags. That is how scorix_generate kind=model came to pass
// an empty schema path and read the project root as a file.
func TestEveryToolAnswersExactlyLikeItsCLICommand(t *testing.T) {
	dir := fixtureProject(t)

	cases := []struct {
		name string
		cli  []string
		tool string
		args map[string]any
	}{
		{"doctor", []string{"doctor"}, "scorix_doctor", nil},
		{"validate", []string{"validate", "-d", dir}, "scorix_validate", map[string]any{"dir": dir}},
		{"generate proto --check", []string{"generate", "proto", "-d", dir, "--check"},
			"scorix_generate", map[string]any{"kind": "proto", "dir": dir, "check": true}},
		{"generate model --check", []string{"generate", "model", "-d", dir, "--check"},
			"scorix_generate", map[string]any{"kind": "model", "dir": dir, "check": true}},
		{"test", []string{"test", "-d", dir}, "scorix_test", map[string]any{"dir": dir}},
		{"lint", []string{"lint", "-d", dir}, "scorix_lint", map[string]any{"dir": dir}},
		{"surface", []string{"surface", "-d", dir}, "scorix_surface", map[string]any{"dir": dir}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cliDoc, _ := runCLI(t, append(c.cli, "--json")...)
			mcpDoc := callTool(t, c.tool, c.args)
			if got, want := canonical(t, mcpDoc), canonical(t, cliDoc); got != want {
				t.Fatalf("the two front ends disagree\n cli = %s\n mcp = %s", want, got)
			}
		})
	}
}

func envelope(t *testing.T, doc string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatalf("not an envelope: %v\n%s", err, doc)
	}
	return m
}

// The loop an agent actually repeats, driven end to end over MCP. `init` and
// `build` stay out on purpose: both need the network and a Go toolchain, and
// what breaks in this loop is the generate/check pair, not the scaffolding.
func TestCodegenLoopOverMCP(t *testing.T) {
	dir := fixtureProject(t)
	gen := func(kind string, check bool) map[string]any {
		return envelope(t, callTool(t, "scorix_generate",
			map[string]any{"kind": kind, "dir": dir, "check": check}))
	}

	if env := envelope(t, callTool(t, "scorix_validate", map[string]any{"dir": dir})); env["ok"] != true {
		t.Fatalf("the fixture should validate: %v", env)
	}
	for _, kind := range []string{"proto", "model"} {
		if env := gen(kind, false); env["ok"] != true {
			t.Fatalf("generate %s: %v", kind, env["error"])
		}
		if env := gen(kind, true); env["ok"] != true {
			t.Fatalf("%s drifted immediately after generating it: %v", kind, env["error"])
		}
	}

	// Adding an rpc must show up as drift, with the files named: an agent that is
	// told only "out of sync" has nothing to act on.
	proto := filepath.Join(dir, "idl", "app.proto")
	src, err := os.ReadFile(proto)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(src),
		"rpc Ping (PingRequest) returns (PingReply);",
		"rpc Ping (PingRequest) returns (PingReply);\n  rpc Pong (PingRequest) returns (PingReply);", 1)
	if err := os.WriteFile(proto, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}

	env := gen("proto", true)
	if env["ok"] != false {
		t.Fatal("an edited proto did not register as drift")
	}
	if env["exit"] != float64(3) {
		t.Fatalf("drift exited %v, want 3 - CI branches on that code", env["exit"])
	}
	drift, _ := env["data"].(map[string]any)["drift"].([]any)
	if len(drift) == 0 {
		t.Fatal("drift reported no files, so there is nothing to act on")
	}

	if env := gen("proto", false); env["ok"] != true {
		t.Fatalf("regenerate after drift: %v", env["error"])
	}
	if env := gen("proto", true); env["ok"] != true {
		t.Fatalf("still drifting after regenerate: %v", env["error"])
	}
}

// A proto that does not parse must not be reported as drift: "regenerate and
// commit" is then advice to commit the damage.
func TestBrokenProtoIsNotReportedAsDrift(t *testing.T) {
	dir := fixtureProject(t)
	proto := filepath.Join(dir, "idl", "app.proto")
	if err := os.WriteFile(proto, []byte(strings.Replace(fixtureProto,
		"returns (PingReply)", "returns (Ghost)", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envelope(t, callTool(t, "scorix_generate", map[string]any{"kind": "proto", "dir": dir, "check": true}))
	if env["exit"] == float64(3) {
		t.Fatal("a parse failure was reported as drift")
	}
	if msg, _ := env["error"].(string); !strings.Contains(msg, "never declares") {
		t.Fatalf("error did not name the cause: %q", msg)
	}
}
