package runner

import (
	"strings"
	"testing"
)

func findMessage(pf protoFile, name string) *protoMessage {
	for i := range pf.Messages {
		if pf.Messages[i].Name == name {
			return &pf.Messages[i]
		}
	}
	return nil
}

func hasField(m *protoMessage, name string) bool {
	if m == nil {
		return false
	}
	for _, f := range m.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

func TestParseProto_FieldsSharingALineAreAllRead(t *testing.T) {
	pf, err := parseProto(`syntax = "proto3";
package demo;
message Node { string name = 1; repeated Node children = 2; int64 size = 3 [deprecated = true]; }
service S {
  rpc Get (Node) returns (Node);
}
`)
	if err != nil {
		t.Fatal(err)
	}
	m := findMessage(pf, "Node")
	for _, name := range []string{"name", "children", "size"} {
		if !hasField(m, name) {
			t.Errorf("field %q was dropped; parsed %+v", name, m)
		}
	}
}

func TestANestedMessageIsRefusedNotFlattened(t *testing.T) {
	src := `
syntax = "proto3";
package app;

message Outer {
  message Inner {
    string a = 1;
  }
  Inner inner = 1;
}
`
	_, err := parseProto(src)
	if err == nil || !strings.Contains(err.Error(), "Inner is declared inside Outer") {
		t.Fatalf("parseProto = %v, want the nested message refused by name", err)
	}
}

// Regression guard: a single-level message parses with every field present and
// correct types.
func TestParseProto_SimpleMessageUnchanged(t *testing.T) {
	src := `
syntax = "proto3";
package app;

message PingReply {
  string status = 1;
  repeated int32 codes = 2;
}

service Healthz {
  rpc Ping (PingReply) returns (PingReply);
}
`
	pf, err := parseProto(src)
	if err != nil {
		t.Fatalf("parseProto: %v", err)
	}
	m := findMessage(pf, "PingReply")
	if m == nil {
		t.Fatal("PingReply not parsed")
	}
	if len(m.Fields) != 2 {
		t.Fatalf("want 2 fields, got %d: %+v", len(m.Fields), m.Fields)
	}
	if m.Fields[0].Name != "status" || m.Fields[0].GoType != "string" {
		t.Errorf("status field wrong: %+v", m.Fields[0])
	}
	if m.Fields[1].Name != "codes" || m.Fields[1].GoType != "[]int32" || !m.Fields[1].Repeated {
		t.Errorf("codes field wrong: %+v", m.Fields[1])
	}
	if len(pf.Services) != 1 || len(pf.Services[0].RPCs) != 1 {
		t.Fatalf("service/RPC parse changed: %+v", pf.Services)
	}
}

func TestAMalformedFieldFailsWithItsLine(t *testing.T) {
	src := `syntax = "proto3";
package app;

message Thing {
  string ok = 1;
  string broken
  int32  also_ok = 2;
}
`
	_, err := parseProto(src)
	if err == nil || !strings.Contains(err.Error(), "app.proto:7:") {
		t.Fatalf("parseProto = %v, want a fatal error at the line after the broken field", err)
	}
}

func TestParseProto_UnsupportedConstructsRejected(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"enum":        {"syntax = \"proto3\";\nenum Color { RED = 0; }\nmessage M { string a = 1; }\nservice S { rpc Do(M) returns (M); }", `"enum" is not supported`},
		"nested enum": {"syntax = \"proto3\";\nmessage M { enum Kind { A = 0; } string a = 1; }\nservice S { rpc Do(M) returns (M); }", `"enum" is not supported`},
		"oneof":       {"syntax = \"proto3\";\nmessage M { oneof pick { string a = 1; int32 b = 2; } }\nservice S { rpc Do(M) returns (M); }", `"oneof" is not supported`},
		"map":         {"syntax = \"proto3\";\nmessage M { map<string, string> tags = 1; }\nservice S { rpc Do(M) returns (M); }", `"map" is not supported`},
		"optional":    {"syntax = \"proto3\";\nmessage M { optional string a = 1; }\nservice S { rpc Do(M) returns (M); }", "field M.a is optional"},
		"first wins":  {"syntax = \"proto3\";\nmessage M { map<string, string> tags = 1; }\nenum Color { RED = 0; }\nservice S { rpc Do(M) returns (M); }", `"map" is not supported`},
	}
	for name, c := range cases {
		if _, err := parseProto(c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
	ok := "syntax = \"proto3\";\n// enum-like values live in a string; map is stitched in logic\nmessage M { string a = 1; }\nservice S { rpc Do(M) returns (M); }"
	if _, err := parseProto(ok); err != nil {
		t.Errorf("comment mentioning constructs rejected: %v", err)
	}
}
