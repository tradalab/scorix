package idl

import (
	"reflect"
	"strings"
	"testing"
)

const fixBefore = `syntax = "proto3";
package acme;

import "scorix/v1/annotations.proto";

message Empty {}
message Req {
  string name = 1;
  string key = 2;
  repeated string args = 3;
}

// Accounts on this machine.
// @middleware audit
service account {
  // Show one.
  // @mcp timeout=2m
  rpc Show (Req) returns (Empty);

  // Remove one.
  // @mcp destructive
  // @cli headless destructive arg=name
  rpc Remove (Req) returns (Empty);

  // Run one.
  // @middleware trace
  // @cli headless exec arg=name rest=args timeout=10m
  rpc Run (Req) returns (Empty) { option (scorix.v1.rpc) = { timeout: "1m" }; }

  // Add one.
  // @cli arg=name secret=key
  rpc Add (Req) returns (Empty) {
    option (scorix.v1.rpc) = { permission: "accounts.write" };
  }

  // Rename one.
  // @cli arg=name
  rpc Rename (Req) returns (Empty) {
    option (scorix.v1.rpc) = { permission: "accounts.write" }; }

  // Purge all.
  // @mcp destructive
  rpc Purge (Req) returns (Empty);

  // @event
  rpc Changed (Req) returns (Empty);
  // @event in
  rpc Typed (Req) returns (Empty);
  // @broadcast
  rpc Ticked (Req) returns (Empty);
  rpc Plain (Req) returns (Empty);
}
`

const fixAfter = `syntax = "proto3";
package acme;

import "scorix/v1/annotations.proto";

message Empty {}
message Req {
  string name = 1;
  string key = 2;
  repeated string args = 3;
}

// Accounts on this machine.
// @middleware audit
service account {
  // Show one.
  rpc Show (Req) returns (Empty) {
    option (scorix.v1.mcp) = { timeout: "2m" };
  }

  // Remove one.
  rpc Remove (Req) returns (Empty) {
    option (scorix.v1.rpc) = { effect: DESTRUCTIVE };
    option (scorix.v1.mcp) = {};
    option (scorix.v1.cli) = { headless: true arg: "name" };
  }

  // Run one.
  // @middleware trace
  rpc Run (Req) returns (Empty) {
    option (scorix.v1.rpc) = { timeout: "1m" };
    option (scorix.v1.cli) = { headless: true exec: true arg: "name" rest: "args" timeout: "10m" };
  }

  // Add one.
  rpc Add (Req) returns (Empty) {
    option (scorix.v1.rpc) = { permission: "accounts.write" };
    option (scorix.v1.cli) = { arg: "name" secret: "key" };
  }

  // Rename one.
  rpc Rename (Req) returns (Empty) {
    option (scorix.v1.rpc) = { permission: "accounts.write" };
    option (scorix.v1.cli) = { arg: "name" };
  }

  // Purge all.
  rpc Purge (Req) returns (Empty) {
    option (scorix.v1.rpc) = { effect: DESTRUCTIVE };
    option (scorix.v1.mcp) = {};
  }

  rpc Changed (Req) returns (Empty) {
    option (scorix.v1.event) = {};
  }
  rpc Typed (Req) returns (Empty) {
    option (scorix.v1.event) = { direction: IN };
  }
  rpc Ticked (Req) returns (Empty) {
    option (scorix.v1.event) = {};
  }
  rpc Plain (Req) returns (Empty);
}
`

func TestFixMovesEveryCommentAnnotationIntoItsTraitAndChangesNothingElse(t *testing.T) {
	out, notes, err := Fix("app.proto", fixBefore)
	if err != nil {
		t.Fatal(err)
	}
	if out != fixAfter {
		t.Fatalf("fixed proto:\n%s", out)
	}
	if len(notes) != 0 {
		t.Errorf("notes for a lossless fix: %v", notes)
	}
	before, _ := Parse("app.proto", fixBefore)
	after, err := Parse("app.proto", out)
	if err != nil {
		t.Fatal(err)
	}
	for i := range before.Services[0].RPCs {
		before.Services[0].RPCs[i].Legacy = nil
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the model moved:\n%+v\n%+v", before, after)
	}
}

func TestFixOfAProtoWithNothingToMoveIsTheSameText(t *testing.T) {
	out, notes, err := Fix("app.proto", fixAfter)
	if err != nil || out != fixAfter || len(notes) != 0 {
		t.Fatalf("second fix = %v %v\n%s", err, notes, out)
	}
	bare := "syntax = \"proto3\";\npackage acme;\nmessage Empty {}\nservice s {\n  // Do it.\n  // @mcp\n  rpc Do (Empty) returns (Empty);\n}\n"
	want := "syntax = \"proto3\";\npackage acme;\n\nimport \"scorix/v1/annotations.proto\";\nmessage Empty {}\nservice s {\n  // Do it.\n  rpc Do (Empty) returns (Empty) {\n    option (scorix.v1.mcp) = {};\n  }\n}\n"
	if out, _, err := Fix("app.proto", bare); err != nil || out != want {
		t.Fatalf("a proto with no import = %v\n%s", err, out)
	}
	plain := "syntax = \"proto3\";\npackage acme;\nmessage Empty {}\nservice s { rpc Do (Empty) returns (Empty); }\n"
	if out, _, _ := Fix("app.proto", plain); out != plain {
		t.Fatalf("a proto with no annotation gained:\n%s", out)
	}
}

func TestFixSaysWhereTheTraitFormIsMoreCarefulThanTheComment(t *testing.T) {
	src := strings.Replace(fixBefore, "  // Show one.\n  // @mcp timeout=2m\n  rpc Show (Req)", "  // Drop one.\n  // @cli\n  rpc DeleteOne (Req)", 1)
	_, notes, err := Fix("app.proto", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "account.DeleteOne") || !strings.Contains(notes[0], "asks before it runs") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestFixSaysWhenAnAgentWillBeToldAToolIsDestructive(t *testing.T) {
	src := strings.Replace(fixBefore, "  // Show one.\n  // @mcp timeout=2m\n  rpc Show (Req)", "  // Drop one.\n  // @mcp\n  rpc DeleteOne (Req)", 1)
	_, notes, err := Fix("app.proto", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "account.DeleteOne") || !strings.Contains(notes[0], "agents are told it is destructive") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestFixGivesADestructiveCommandTheEffectItAlwaysHad(t *testing.T) {
	src := strings.Replace(fixBefore, "  // @mcp destructive\n  // @cli headless destructive arg=name\n", "  // @cli headless destructive arg=name\n", 1)
	out, notes, err := Fix("app.proto", src)
	if err != nil || len(notes) != 0 {
		t.Fatalf("fix = %v %v", err, notes)
	}
	if !strings.Contains(out, "  rpc Remove (Req) returns (Empty) {\n    option (scorix.v1.rpc) = { effect: DESTRUCTIVE };\n    option (scorix.v1.cli) = { headless: true arg: \"name\" };\n  }") {
		t.Fatalf("Remove =\n%s", out)
	}
}

func TestFixRefusesWhatItCannotMoveByItself(t *testing.T) {
	src := strings.Replace(fixBefore, "  // @cli arg=name secret=key\n", "  // @cli destructive arg=name secret=key\n", 1)
	if _, _, err := Fix("app.proto", src); err == nil || !strings.Contains(err.Error(), `account.Add has a (scorix.v1.rpc) option; add effect: DESTRUCTIVE to it by hand`) {
		t.Fatalf("err = %v", err)
	}
}

const fixHead = "syntax = \"proto3\";\npackage acme;\nimport \"scorix/v1/annotations.proto\";\nmessage Empty {}\n"

func TestFixRewritesAnRPCIndentedWithTabs(t *testing.T) {
	src := fixHead + "service s {\n\t// Do it.\n\t// @mcp\n\trpc Do (Empty) returns (Empty);\n}\n"
	want := fixHead + "service s {\n\t// Do it.\n\trpc Do (Empty) returns (Empty) {\n\t  option (scorix.v1.mcp) = {};\n\t}\n}\n"
	if out, _, err := Fix("app.proto", src); err != nil || out != want {
		t.Fatalf("fix = %v\n%s", err, out)
	}
}

func TestFixRewritesAnRPCWhoseLineHoldsNonASCIIText(t *testing.T) {
	src := fixHead + "service s {\n  // Do it.\n  // @mcp\n  rpc Do (Empty) returns (Empty) { option (scorix.v1.rpc) = { permission: \"é.read\" }; }\n}\n"
	want := fixHead + "service s {\n  // Do it.\n  rpc Do (Empty) returns (Empty) {\n    option (scorix.v1.rpc) = { permission: \"é.read\" };\n    option (scorix.v1.mcp) = {};\n  }\n}\n"
	if out, _, err := Fix("app.proto", src); err != nil || out != want {
		t.Fatalf("fix = %v\n%s", err, out)
	}
}

func TestFixGivesTheCommentAboveALineOnlyToTheFirstRPCOnIt(t *testing.T) {
	src := fixHead + "service s {\n  // Do it.\n  // @mcp\n  rpc A (Empty) returns (Empty); rpc B (Empty) returns (Empty);\n}\n"
	want := fixHead + "service s {\n  // Do it.\n  rpc A (Empty) returns (Empty) {\n    option (scorix.v1.mcp) = {};\n  } rpc B (Empty) returns (Empty);\n}\n"
	if out, _, err := Fix("app.proto", src); err != nil || out != want {
		t.Fatalf("fix = %v\n%s", err, out)
	}
}

func TestFixKeepsTheCRLFLineEndingsOfAWindowsProto(t *testing.T) {
	src := strings.ReplaceAll(fixHead+"service s {\n  // Do it.\n  // @mcp\n  rpc Do (Empty) returns (Empty);\n}\n", "\n", "\r\n")
	want := strings.ReplaceAll(fixHead+"service s {\n  // Do it.\n  rpc Do (Empty) returns (Empty) {\n    option (scorix.v1.mcp) = {};\n  }\n}\n", "\n", "\r\n")
	if out, _, err := Fix("app.proto", src); err != nil || out != want {
		t.Fatalf("fix = %v\n%q", err, out)
	}
}

func TestFixRefusesAnAnnotationInABlockCommentItCannotMove(t *testing.T) {
	src := fixHead + "service s {\n  /* Do it.\n  @mcp */\n  rpc Do (Empty) returns (Empty);\n}\n"
	if _, _, err := Fix("app.proto", src); err == nil || !strings.Contains(err.Error(), "rpc s.Do has @mcp in a /* */ comment") {
		t.Fatalf("err = %v", err)
	}
}
