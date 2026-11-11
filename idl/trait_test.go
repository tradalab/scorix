package idl

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const traitHead = `syntax = "proto3";
package acme;
import "scorix/v1/annotations.proto";
message Empty {}
message Req {
  string name = 1;
  string key = 2;
  repeated string args = 3;
}
`

func parsed(t *testing.T, body string) map[string]RPC {
	t.Helper()
	f, err := Parse("app.proto", traitHead+body)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]RPC{}
	for _, s := range f.Services {
		for _, r := range s.RPCs {
			out[r.Name] = r
		}
	}
	return out
}

func TestTheTraitFormSaysWhatTheCommentFormSaid(t *testing.T) {
	comments := parsed(t, `service account {
  // Show one.
  // @mcp timeout=2m
  rpc Show (Req) returns (Empty);
  // Remove one.
  // @mcp destructive
  // @cli headless destructive arg=name
  rpc Remove (Req) returns (Empty);
  // Run one.
  // @cli headless exec arg=name rest=args timeout=10m
  rpc Run (Req) returns (Empty);
  // Add one.
  // @cli arg=name secret=key
  rpc Add (Req) returns (Empty);
  // @event
  rpc Changed (Req) returns (Empty);
  // @event in
  rpc Typed (Req) returns (Empty);
  // @broadcast
  rpc Ticked (Req) returns (Empty);
}`)
	traits := parsed(t, `service account {
  // Show one.
  rpc Show (Req) returns (Empty) { option (scorix.v1.mcp) = { timeout: "2m" }; }
  // Remove one.
  rpc Remove (Req) returns (Empty) {
    option (scorix.v1.rpc) = { effect: DESTRUCTIVE };
    option (scorix.v1.mcp) = {};
    option (scorix.v1.cli) = { headless: true arg: "name" };
  }
  // Run one.
  rpc Run (Req) returns (Empty) { option (scorix.v1.cli) = { headless: true exec: true arg: "name" rest: "args" timeout: "10m" }; }
  // Add one.
  rpc Add (Req) returns (Empty) { option (scorix.v1.cli) = { arg: "name" secret: "key" }; }
  rpc Changed (Req) returns (Empty) { option (scorix.v1.event) = {}; }
  rpc Typed (Req) returns (Empty) { option (scorix.v1.event) = { direction: IN }; }
  rpc Ticked (Req) returns (Empty) { option (scorix.v1.event) = { direction: OUT }; }
}`)
	for name, c := range comments {
		tr := traits[name]
		if len(c.Legacy) == 0 || len(tr.Legacy) != 0 {
			t.Errorf("%s: legacy %v from comments, %v from traits", name, c.Legacy, tr.Legacy)
		}
		c.Legacy = nil
		if !reflect.DeepEqual(c, tr) {
			t.Errorf("%s:\ncomment %+v\ntrait   %+v", name, c, tr)
		}
	}
	if r := traits["Show"]; !r.MCP || r.MCPTimeout != 2*time.Minute || r.MCPDestructive {
		t.Errorf("Show = %+v", r)
	}
	if r := traits["Typed"]; !r.IsEvent || r.EventDir != "in" {
		t.Errorf("Typed = %+v", r)
	}
}

func TestATraitToolOrCommandIsDestructiveExactlyWhenItsEffectIs(t *testing.T) {
	r := parsed(t, `service s {
  // Drop it.
  rpc DeleteThing (Req) returns (Empty) { option (scorix.v1.mcp) = {}; option (scorix.v1.cli) = {}; }
  // Keep it.
  rpc SaveThing (Req) returns (Empty) { option (scorix.v1.mcp) = {}; option (scorix.v1.cli) = {}; }
}`)
	if d := r["DeleteThing"]; !d.MCPDestructive || !d.CLIDestructive {
		t.Errorf("a Delete rpc = %+v", d)
	}
	if s := r["SaveThing"]; s.MCPDestructive || s.CLIDestructive {
		t.Errorf("a Save rpc = %+v", s)
	}
}

func TestAnAnnotationSaidTwiceOrBadlyIsRefused(t *testing.T) {
	for name, c := range map[string]struct{ svc, want string }{
		"mcp twice":   {"service s {\n  // Do.\n  // @mcp\n  rpc Do (Req) returns (Empty) { option (scorix.v1.mcp) = {}; }\n}", "rpc s.Do says @mcp twice, as a comment and as (scorix.v1.mcp); keep the option"},
		"cli twice":   {"service s {\n  // Do.\n  // @cli\n  rpc Do (Req) returns (Empty) { option (scorix.v1.cli) = {}; }\n}", "says @cli twice"},
		"event twice": {"service s {\n  // @broadcast\n  rpc Do (Req) returns (Empty) { option (scorix.v1.event) = {}; }\n}", "says @event twice"},
		"mcp timeout": {`service s { rpc Do (Req) returns (Empty) { option (scorix.v1.mcp) = { timeout: "later" }; } }`, `(scorix.v1.mcp) timeout "later" is not a positive Go duration`},
		"cli timeout": {`service s { rpc Do (Req) returns (Empty) { option (scorix.v1.cli) = { timeout: "-1s" }; } }`, `(scorix.v1.cli) timeout "-1s"`},
	} {
		if _, err := Parse("app.proto", traitHead+c.svc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
