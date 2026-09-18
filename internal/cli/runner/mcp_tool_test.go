package runner

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMCPDestructiveModifier(t *testing.T) {
	pf := parseService(t, `
  // Removes every staged file.
  // @mcp destructive
  rpc Wipe (Req) returns (Res);

  // Lists staged files.
  // @mcp
  rpc List (Req) returns (Res);
`)
	rpcs := pf.Services[0].RPCs
	if !rpcs[0].MCP || !rpcs[0].MCPDestructive {
		t.Errorf("`@mcp destructive` = %+v, want an opted-in destructive tool", rpcs[0])
	}
	if !rpcs[1].MCP || rpcs[1].MCPDestructive {
		t.Errorf("plain `@mcp` = %+v, want an opted-in tool that is not destructive", rpcs[1])
	}
}

// A misspelt modifier must not fall through to "not a tool" or "not destructive".
func TestUnknownMCPModifierIsRefused(t *testing.T) {
	_, err := parseProto(mcpProtoHead + `
  // Removes every staged file.
  // @mcp destroy
  rpc Wipe (Req) returns (Res);
` + "}\n")
	if err == nil || !strings.Contains(err.Error(), "destroy") {
		t.Fatalf("`@mcp destroy` was accepted or not named: %v", err)
	}
}

func TestTrailingMCPModifierIsRejected(t *testing.T) {
	_, err := parseProto(mcpProtoHead + `
  rpc Wipe (Req) returns (Res); // @mcp destructive

  rpc Next (Req) returns (Res);
` + "}\n")
	if err == nil {
		t.Fatal("a trailing `@mcp destructive` was accepted, so it lands on the next rpc")
	}
}

func TestMCPDocIsTheCommentWithoutAnnotations(t *testing.T) {
	pf := parseService(t, `
  // Deletes every key matching a pattern.
  //   Use with care.
  // @middleware Auth
  // @mcp destructive
  rpc Wipe (Req) returns (Res);
`)
	if got, want := pf.Services[0].RPCs[0].Doc, "Deletes every key matching a pattern. Use with care."; got != want {
		t.Errorf("doc = %q, want %q", got, want)
	}
}

func TestMCPWithoutADescriptionIsRefused(t *testing.T) {
	pf := parseService(t, `
  // @mcp
  rpc Clean (Req) returns (Res);
`)
	if _, _, err := enrichProto(&pf); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("an undocumented @mcp rpc was accepted: %v", err)
	}
}

func TestMCPToolNamesAreHeldToWhatClientsAccept(t *testing.T) {
	long := parseService(t, "\n  // Has a name no client accepts.\n  // @mcp\n  rpc "+strings.Repeat("Clean", 14)+" (Req) returns (Res);\n")
	if _, _, err := enrichProto(&long); err == nil || !strings.Contains(err.Error(), "1-64") {
		t.Errorf("a 70-character tool name was accepted: %v", err)
	}
	twice := parseService(t, `
  // Frees disk space.
  // @mcp
  rpc Clean (Req) returns (Res);

  // Frees disk space too.
  // @mcp
  rpc clean (Req) returns (Res);
`)
	if _, _, err := enrichProto(&twice); err == nil || !strings.Contains(err.Error(), "both become the MCP tool") {
		t.Errorf("two rpcs landing on one tool name were accepted: %v", err)
	}
}

const mcpSchemaProto = `syntax = "proto3";
package demo;

message Filter { string pattern = 1; repeated string types = 2; }
message Node { string name = 1; repeated Node children = 2; }
message ScanReq {
  string connection_id = 1;
  int64 limit = 2;
  bool dry_run = 3;
  double ratio = 4;
  bytes blob = 5;
  Filter filter = 6;
  repeated Filter extra = 7;
  Node tree = 8;
}
message ScanRes { repeated string keys = 1; }

service Keys {
  // Scans keys.
  // @mcp
  rpc Scan (ScanReq) returns (ScanRes);
}
`

func TestMCPInputSchemaFollowsTheGeneratedJSONNames(t *testing.T) {
	pf, err := parseProto(mcpSchemaProto)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := enrichProto(&pf); err != nil {
		t.Fatal(err)
	}
	rpc := pf.Services[0].RPCs[0]
	if rpc.MCPToolName != "keys_scan" {
		t.Errorf("tool name = %q, want keys_scan", rpc.MCPToolName)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(rpc.MCPSchema), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v\n%s", err, rpc.MCPSchema)
	}
	props := schema["properties"].(map[string]any)
	typeOf := func(name string) string {
		p, ok := props[name].(map[string]any)
		if !ok {
			t.Fatalf("no property %q in %s", name, rpc.MCPSchema)
		}
		s, _ := p["type"].(string)
		return s
	}
	for name, want := range map[string]string{
		"connection_id": "string", "limit": "integer", "dry_run": "boolean", "ratio": "number",
		"blob": "string", "filter": "object", "extra": "array", "tree": "object",
	} {
		if got := typeOf(name); got != want {
			t.Errorf("%s: type %q, want %q", name, got, want)
		}
	}
	if enc := props["blob"].(map[string]any)["contentEncoding"]; enc != "base64" {
		t.Errorf("bytes field has contentEncoding %v: Go decodes []byte from base64", enc)
	}
	filterProps := props["filter"].(map[string]any)["properties"].(map[string]any)
	if _, ok := filterProps["pattern"]; !ok {
		t.Errorf("nested message lost its fields: %v", props["filter"])
	}
	if schema["additionalProperties"] != false || props["filter"].(map[string]any)["additionalProperties"] != false {
		t.Errorf("the request schema is open, so a misspelt argument goes through: %s", rpc.MCPSchema)
	}
	items := props["extra"].(map[string]any)["items"].(map[string]any)
	if items["type"] != "object" {
		t.Errorf("repeated message items = %v, want objects", items)
	}
	children := props["tree"].(map[string]any)["properties"].(map[string]any)["children"].(map[string]any)
	if inner := children["items"].(map[string]any); inner["properties"] != nil || inner["additionalProperties"] != nil {
		t.Errorf("a self-referencing message was expanded again, or closed with nothing to name: %v", inner)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(rpc.MCPOutputSchema), &out); err != nil {
		t.Fatalf("output schema is not JSON: %v\n%s", err, rpc.MCPOutputSchema)
	}
	keys, ok := out["properties"].(map[string]any)["keys"].(map[string]any)
	if !ok || keys["type"] != "array" {
		t.Errorf("output schema does not describe ScanRes.keys: %s", rpc.MCPOutputSchema)
	}
}

func TestMCPTimeoutModifier(t *testing.T) {
	pf := parseService(t, `
  // Scans the whole disk.
  // @mcp destructive timeout=30m
  rpc Scan (Req) returns (Res);
`)
	if rpc := pf.Services[0].RPCs[0]; rpc.MCPTimeout != 30*time.Minute || !rpc.MCPDestructive {
		t.Errorf("`@mcp destructive timeout=30m` = timeout %s, destructive %v", rpc.MCPTimeout, rpc.MCPDestructive)
	}
	for _, bad := range []string{"timeout=soon", "timeout=0s", "timeout=-1m"} {
		if _, err := parseProto(mcpProtoHead + "\n  // Scans.\n  // @mcp " + bad + "\n  rpc Scan (Req) returns (Res);\n}\n"); err == nil {
			t.Errorf("`@mcp %s` was accepted", bad)
		}
	}
	if _, err := parseProto(mcpProtoHead + "\n  rpc Scan (Req) returns (Res); // @mcp timeout=30m\n\n  rpc Next (Req) returns (Res);\n}\n"); err == nil {
		t.Error("a trailing `@mcp timeout=30m` was accepted, so it lands on the next rpc")
	}
}

func TestGenerateRegistersMCPTools(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "idl"), 0o755); err != nil {
		t.Fatal(err)
	}
	proto := strings.Replace(mcpSchemaProto, "  // @mcp\n", "  // @mcp destructive timeout=10m\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "idl", "app.proto"), []byte(proto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("GenerateProto: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "internal", "handler", "handler.go"))
	if err != nil {
		t.Fatal(err)
	}
	// Whitespace collapsed: gofmt realigns the literal whenever a longer key joins it.
	h := strings.Join(strings.Fields(string(b)), " ")
	for _, want := range []string{
		"a.MCPTool(app.MCPTool{",
		`Name: "keys_scan",`,
		`Command: "keys:scan",`,
		`Description: "Scans keys.",`,
		"InputSchema: json.RawMessage(",
		"OutputSchema: json.RawMessage(",
		"Destructive: true,",
		"Timeout: 600000000000, // 10m0s",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("handler.go is missing %q:\n%s", want, b)
		}
	}
	for file, wants := range map[string][]string{
		"shell/api/index.ts":   {"export const scorixMCP", `"sys:mcp:status"`, `"sys:mcp:enable"`, `"sys:mcp:tool"`, `"sys:mcp:revoke"`, `"sys:mcp:call"`},
		"shell/types/index.ts": {"export interface ScorixMCPStatus", "export interface ScorixMCPClient", "client_path: string", "export interface ScorixMCPCall"},
	} {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s is missing %q:\n%s", file, want, b)
			}
		}
	}
	docs, err := os.ReadFile(filepath.Join(dir, "docs", "ipc-surface.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docs), "| destructive |") {
		t.Errorf("ipc-surface.md does not mark the destructive tool:\n%s", docs)
	}
}

// Six apps have no @mcp at all; the handler they regenerate must not change.
func TestGenerateWithoutMCPEmitsNoToolCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/demo\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "idl"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "idl", "app.proto"), []byte(eventsProto), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatalf("GenerateProto: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "internal", "handler", "handler.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "MCP") {
		t.Errorf("a proto with no @mcp generated tool code:\n%s", b)
	}
	for _, file := range []string{"shell/api/index.ts", "shell/types/index.ts"} {
		ts, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(ts), "MCP") {
			t.Errorf("%s of a proto with no @mcp carries the MCP client:\n%s", file, ts)
		}
	}
}
