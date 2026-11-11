package idl

import (
	"strings"
	"testing"
	"time"
)

func rpcs(t *testing.T, svc string) map[string]RPC {
	t.Helper()
	f, err := Parse("app.proto", httpHead+svc)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]RPC{}
	for _, r := range f.Services[0].RPCs {
		out[r.Name] = r
	}
	return out
}

func TestAnRPCsEffectIsWhatItSaysElseWhatItsNameSays(t *testing.T) {
	r := rpcs(t, `service S {
  rpc GetThing (Req) returns (Empty);
  rpc ListThings (Req) returns (Empty);
  rpc DeleteThing (Req) returns (Empty);
  rpc Getaway (Req) returns (Empty);
  rpc Get (Req) returns (Empty);
  rpc GetOrCreateSession (Req) returns (Empty);
  rpc GetAndIncrement (Req) returns (Empty);
  rpc ListOrders (Req) returns (Empty);
  rpc GetAndroidBuild (Req) returns (Empty);
  rpc MakeThing (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: IDEMPOTENT }; }
  rpc GetCounter (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: MUTATING }; }
  // Wipe it.
  // @mcp destructive
  rpc Wipe (Req) returns (Empty);
}`)
	for name, want := range map[string]string{
		"GetThing": "readonly", "ListThings": "readonly", "DeleteThing": "destructive", "Getaway": "", "Get": "readonly",
		"GetOrCreateSession": "", "GetAndIncrement": "", "ListOrders": "readonly", "GetAndroidBuild": "readonly",
		"MakeThing": "idempotent", "GetCounter": "mutating", "Wipe": "destructive",
	} {
		if got := r[name].Effect; got != want {
			t.Errorf("%s effect = %q, want %q", name, got, want)
		}
	}
}

func TestATimeoutAndAPermissionAreRead(t *testing.T) {
	r := rpcs(t, `service S { rpc Do (Req) returns (Empty) { option (scorix.v1.rpc) = { timeout: "250ms" permission: "things.write" }; } }`)
	if r["Do"].Timeout != 250*time.Millisecond || r["Do"].Permission != "things.write" {
		t.Fatalf("Do = %+v", r["Do"])
	}
}

func TestAContractThatCannotHoldIsRefused(t *testing.T) {
	for name, c := range map[string]struct{ svc, want string }{
		"destructive readonly": {"service S {\n  // Look.\n  // @mcp destructive\n  rpc GetThing (Req) returns (Empty);\n}", "S.GetThing is @mcp destructive but its effect is READONLY"},
		"destructive explicit": {"service S {\n  // Look.\n  // @mcp destructive\n  rpc Do (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: MUTATING }; }\n}", "effect is MUTATING"},
		"bad timeout":          {`service S { rpc Do (Req) returns (Empty) { option (scorix.v1.rpc) = { timeout: "soon" }; } }`, `timeout "soon" is not a positive Go duration`},
		"zero timeout":         {`service S { rpc Do (Req) returns (Empty) { option (scorix.v1.rpc) = { timeout: "0s" }; } }`, `timeout "0s"`},
		"spaced permission":    {`service S { rpc Do (Req) returns (Empty) { option (scorix.v1.rpc) = { permission: "things write" }; } }`, `permission "things write" holds whitespace`},
	} {
		if _, err := Parse("app.proto", httpHead+c.svc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestReplayIsReadAndRefusedWhereARetryCannotRepeatAnything(t *testing.T) {
	r := rpcs(t, `service S { rpc Make (Req) returns (Empty) { option (scorix.v1.rpc) = { replay: true }; } }`)
	if !r["Make"].Replay {
		t.Fatalf("Make = %+v", r["Make"])
	}
	for name, c := range map[string]struct{ svc, want string }{
		"readonly": {`service S { rpc GetThing (Req) returns (Empty) { option (scorix.v1.rpc) = { replay: true }; } }`, "S.GetThing replays but is READONLY; a read can simply be retried"},
		"stream":   {`service S { rpc Make (Req) returns (stream Empty) { option (scorix.v1.rpc) = { replay: true }; } }`, "S.Make replays but streams its reply; only a single reply can be kept"},
	} {
		if _, err := Parse("app.proto", httpHead+c.svc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestAnRPCIsAuditedUnlessItIsAReadOrSaysOtherwise(t *testing.T) {
	r := rpcs(t, `service S {
  rpc GetThing (Req) returns (Empty);
  rpc Resolve (Req) returns (Empty);
  rpc Save (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: MUTATING }; }
  rpc Peek (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: READONLY }; }
  rpc Click (Req) returns (Empty) { option (scorix.v1.rpc) = { audit: false }; }
  rpc GetSecret (Req) returns (Empty) { option (scorix.v1.rpc) = { audit: true }; }
  rpc Touch (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: MUTATING audit: true }; }
}`)
	for name, want := range map[string]bool{"GetThing": false, "Resolve": true, "Save": true, "Peek": false, "Click": false, "GetSecret": true, "Touch": true} {
		if r[name].Audit != want {
			t.Errorf("%s.Audit = %v, want %v", name, r[name].Audit, want)
		}
	}
}
