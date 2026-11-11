package idl

import (
	"strings"
	"testing"
)

const lintHead = `syntax = "proto3";
package acme.v1;
import "scorix/v1/annotations.proto";
message Empty {}
`

func lints(t *testing.T, body string) []string {
	t.Helper()
	f, err := Parse("app.proto", lintHead+body)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range LintFile(f) {
		out = append(out, l.String())
	}
	return out
}

func TestAProtoThatFollowsTheAIPsHasNoLint(t *testing.T) {
	got := lints(t, `message ListThingsReq {
  int32 page_size = 1;
  string page_token = 2;
  string owner_id = 3;
}
message Thing { string id = 1; }
message ListThingsResp {
  repeated Thing things = 1;
  string next_page_token = 2;
}
service system {
  rpc ListThings (ListThingsReq) returns (ListThingsResp) {
    option (scorix.v1.http) = { get: "/v1/things" };
    option (scorix.v1.list) = { default_page_size: 20 max_page_size: 100 order: "id asc" };
  }
  rpc ListOrCreate (Empty) returns (Empty) { option (scorix.v1.http) = { post: "/v1/x" body: "*" }; }
  rpc ListLocal (Empty) returns (Empty);
}
`)
	if len(got) != 0 {
		t.Fatalf("lint on a clean proto: %v", got)
	}
}

func TestEachAIPStrayIsNamedByItsRule(t *testing.T) {
	got := strings.Join(lints(t, `message bad_name { string ownerId = 1; string ok_2 = 2; string owner_ = 3; string place__id = 4; }
message Req {
  string cursor = 1;
  int32 limit = 2;
}
message Resp {
  string next_cursor = 1;
  int32 total = 2;
}
service S {
  rpc ListThings (Req) returns (Resp) { option (scorix.v1.http) = { get: "/v1/things" }; }
  rpc do_it (Empty) returns (Empty);
}
`), "\n")
	for _, want := range []string{
		"aip-190: message bad_name is not UpperCamelCase",
		"aip-140: message bad_name field ownerId is not lower_snake_case",
		"aip-140: message bad_name field owner_ is not lower_snake_case",
		"aip-140: message bad_name field place__id is not lower_snake_case",
		"aip-190: rpc S.do_it is not UpperCamelCase",
		"aip-158: rpc S.ListThings lists behind a REST route without (scorix.v1.list)",
		"aip-158: rpc S.ListThings Req.cursor is page_token in AIP-158",
		"aip-158: rpc S.ListThings Req.limit is page_size in AIP-158",
		"aip-158: rpc S.ListThings Resp.next_cursor is next_page_token in AIP-158",
		"aip-158: rpc S.ListThings Resp.total is total_size in AIP-158",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ok_2") {
		t.Errorf("a digit word was flagged:\n%s", got)
	}
}
