package runner

import "testing"

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

// L9: a nested `{}` (inline option block) must not truncate the body at the first
// inner `}` — fields after the nested braces must keep parsing.
func TestParseProto_NestedBracesDoNotTruncateFields(t *testing.T) {
	src := `
syntax = "proto3";
package app;

message Outer {
  string before = 1 [(validate.rules).string = {min_len: 1, max_len: 5}];
  int32  after  = 2;
}

service Svc {
  rpc Do (Outer) returns (Outer);
}
`
	pf, err := parseProto(src)
	if err != nil {
		t.Fatalf("parseProto: %v", err)
	}
	outer := findMessage(pf, "Outer")
	if outer == nil {
		t.Fatal("Outer message not parsed")
	}
	if !hasField(outer, "before") {
		t.Errorf("field `before` missing; fields=%+v", outer.Fields)
	}
	// The load-bearing assertion: the field AFTER the nested `{...}` survived.
	if !hasField(outer, "after") {
		t.Errorf("field `after` lost — body truncated at nested brace; fields=%+v", outer.Fields)
	}
}

// L9: a message containing a nested message definition still parses the outer
// fields that follow it.
func TestParseProto_NestedMessageBody(t *testing.T) {
	src := `
syntax = "proto3";
package app;

message Outer {
  message Inner {
    string a = 1;
  }
  Inner inner = 1;
  string tail = 2;
}
`
	pf, err := parseProto(src)
	if err != nil {
		t.Fatalf("parseProto: %v", err)
	}
	outer := findMessage(pf, "Outer")
	if outer == nil {
		t.Fatal("Outer message not parsed")
	}
	if !hasField(outer, "inner") {
		t.Errorf("field `inner` missing; fields=%+v", outer.Fields)
	}
	if !hasField(outer, "tail") {
		t.Errorf("field `tail` lost after nested message; fields=%+v", outer.Fields)
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

// L10: a malformed field line is skipped (good fields still parse, bad one
// dropped); the warning goes to stdout — here we assert the parse stays robust.
func TestParseProto_MalformedFieldSkippedNotFatal(t *testing.T) {
	src := `
syntax = "proto3";
package app;

message Thing {
  string ok = 1;
  string broken
  int32  also_ok = 2;
}
`
	pf, err := parseProto(src)
	if err != nil {
		t.Fatalf("parseProto must not error on a malformed field: %v", err)
	}
	m := findMessage(pf, "Thing")
	if m == nil {
		t.Fatal("Thing not parsed")
	}
	if !hasField(m, "ok") || !hasField(m, "also_ok") {
		t.Errorf("valid fields lost around a malformed line; fields=%+v", m.Fields)
	}
	if hasField(m, "broken") {
		t.Errorf("malformed line should not produce a field; fields=%+v", m.Fields)
	}
}
