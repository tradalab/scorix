package runner

import (
	"strings"
	"testing"
)

const mcpProtoHead = `syntax = "proto3";
package testapp;

message Req {}
message Res { string ok = 1; }

service Tools {
`

func parseService(t *testing.T, body string) protoFile {
	t.Helper()
	pf, err := parseProto(mcpProtoHead + body + "}\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return pf
}

func TestMCPAnnotationOptsACommandIn(t *testing.T) {
	pf := parseService(t, `
  // @mcp
  rpc Clean (Req) returns (Res);

  rpc Quiet (Req) returns (Res);
`)
	rpcs := pf.Services[0].RPCs
	if len(rpcs) != 2 {
		t.Fatalf("rpcs = %+v", rpcs)
	}
	if !rpcs[0].MCP {
		t.Error("the annotated rpc is not opted in")
	}
	if rpcs[1].MCP {
		t.Error("an rpc with no annotation was opted in, so the marker leaked to its neighbour")
	}
}

func TestTrailingMCPAnnotationIsRejected(t *testing.T) {
	_, err := parseProto(mcpProtoHead + `
  rpc Clean (Req) returns (Res); // @mcp

  rpc Wipe (Req) returns (Res);
` + "}\n")
	if err == nil {
		t.Fatal("a trailing @mcp was accepted, so the opt-in lands on the wrong rpc")
	}
	if !strings.Contains(err.Error(), "@mcp") {
		t.Errorf("the error does not name the annotation: %v", err)
	}
}

// Found by probing the built CLI: `\b` matched the annotation inside a word, so a
// comment that only MENTIONS one changed what the rpc is. An event annotation is
// the worse half - it drops the rpc from the command surface entirely.
func TestAnnotationInsideProseIsNotAnAnnotation(t *testing.T) {
	pf := parseService(t, `
  // see @mcp-host design for why this is not a tool
  rpc Plain (Req) returns (Res);

  // see @event-bus design, not an event
  rpc AlsoPlain (Req) returns (Res);

  // @broadcasting is not @broadcast
  rpc Third (Req) returns (Res);
`)
	rpcs := pf.Services[0].RPCs
	if len(rpcs) != 3 {
		t.Fatalf("prose reclassified an rpc: %+v", rpcs)
	}
	for _, r := range rpcs {
		if r.MCP {
			t.Errorf("%s: prose opted it in as a tool", r.Name)
		}
		if r.IsEvent {
			t.Errorf("%s: prose turned it into an event, so it left the command surface", r.Name)
		}
	}
}

func TestRealAnnotationSpellingsStillParse(t *testing.T) {
	pf, err := parseProto(mcpProtoHead + "\n    // @event in\n    rpc Ack (Req) returns (Res);\r\n\n  // @broadcast\r\n  rpc Tick (Req) returns (Res);\n" + "}\n")
	if err != nil {
		t.Fatal(err)
	}
	rpcs := pf.Services[0].RPCs
	if len(rpcs) != 2 {
		t.Fatalf("rpcs = %+v", rpcs)
	}
	if !rpcs[0].IsEvent || rpcs[0].EventDir != "in" {
		t.Errorf("`// @event in` lost its direction: %+v", rpcs[0])
	}
	if !rpcs[1].IsEvent || rpcs[1].EventDir != "out" {
		t.Errorf("`// @broadcast` with CRLF was not read: %+v", rpcs[1])
	}
}

func TestMCPOnAnEventIsRefused(t *testing.T) {
	pf := parseService(t, `
  // @mcp
  // @event
  rpc Tick (Req) returns (Res);
`)
	if _, _, err := enrichProto(&pf); err == nil {
		t.Fatal("@mcp on an event was accepted: a tool call has a reply, an event has none")
	}
}

func TestMCPOnAStreamIsRefused(t *testing.T) {
	pf := parseService(t, `
  // @mcp
  rpc Watch (Req) returns (stream Res);
`)
	if _, _, err := enrichProto(&pf); err == nil {
		t.Fatal("@mcp on a server-stream was accepted: an MCP tool call answers once")
	}
}

func TestSurfaceCarriesTheMCPFlag(t *testing.T) {
	pf := parseService(t, `
  // @mcp
  rpc Clean (Req) returns (Res);

  rpc Quiet (Req) returns (Res);
`)
	if _, _, err := enrichProto(&pf); err != nil {
		t.Fatal(err)
	}
	cmds := ipcSurfaceOf(pf).Services[0].Commands
	if len(cmds) != 2 {
		t.Fatalf("commands = %+v", cmds)
	}
	if !cmds[0].MCP || cmds[1].MCP {
		t.Errorf("surface lost the opt-in: %+v", cmds)
	}
}
