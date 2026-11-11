package idl

import (
	"strings"
	"testing"
)

const listHead = `syntax = "proto3";
package acme;
import "scorix/v1/annotations.proto";
message Thing { string id = 1; }
message ListReq { int32 page_size = 1; string page_token = 2; string query = 3; }
message ListRes { repeated Thing things = 1; string next_page_token = 2; }
`

func TestAListOptionDescribesThePage(t *testing.T) {
	f, err := Parse("app.proto", listHead+`service S { rpc ListThings (ListReq) returns (ListRes) { option (scorix.v1.list) = { default_page_size: 50 max_page_size: 200 order: "created_at desc, id" }; } }`)
	if err != nil {
		t.Fatal(err)
	}
	l := f.Services[0].RPCs[0].List
	if l == nil || l.Default != 50 || l.Max != 200 || len(l.Order) != 2 || l.Order[0] != (Order{"created_at", true}) || l.Order[1] != (Order{"id", false}) {
		t.Fatalf("list = %+v", l)
	}
}

func TestAListThatCannotPageIsRefused(t *testing.T) {
	opt := func(o string) string {
		return `service S { rpc ListThings (ListReq) returns (ListRes) { option (scorix.v1.list) = { ` + o + ` }; } }`
	}
	ok := `default_page_size: 50 max_page_size: 200 order: "created_at desc, id"`
	for name, c := range map[string]struct{ src, want string }{
		"no default":     {listHead + opt(`max_page_size: 200 order: "id"`), "default_page_size must be positive"},
		"max below":      {listHead + opt(`default_page_size: 50 max_page_size: 10 order: "id"`), "max_page_size 10 is below default_page_size 50"},
		"no tiebreaker":  {listHead + opt(`default_page_size: 50 max_page_size: 200 order: "created_at desc"`), "no unique tiebreaker"},
		"tie on last":    {listHead + opt(`default_page_size: 50 max_page_size: 200 order: "created_at desc, name asc"`), "no unique tiebreaker"},
		"bad direction":  {listHead + opt(`default_page_size: 50 max_page_size: 200 order: "created_at down, id"`), "each part is a column, then asc or desc"},
		"empty part":     {listHead + opt(`default_page_size: 50 max_page_size: 200 order: "created_at,, id"`), "each part is a column"},
		"odd column":     {listHead + opt(`default_page_size: 50 max_page_size: 200 order: "CreatedAt, id"`), "each part is a column"},
		"size not int32": {strings.Replace(listHead, "int32 page_size", "int64 page_size", 1) + opt(ok), "needs int32 page_size and string page_token"},
		"no token":       {strings.Replace(listHead, "string page_token = 2;", "", 1) + opt(ok), "needs int32 page_size and string page_token"},
		"no next":        {strings.Replace(listHead, "string next_page_token = 2;", "", 1) + opt(ok), "needs string next_page_token"},
		"two lists":      {strings.Replace(listHead, "string next_page_token = 2;", "string next_page_token = 2; repeated Thing more = 3;", 1) + opt(ok), "has 2 repeated message fields"},
	} {
		if _, err := Parse("app.proto", c.src); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if _, err := Parse("app.proto", listHead+opt(`default_page_size: 5 max_page_size: 5 order: "id desc"`)); err != nil {
		t.Errorf("a single id column is its own tiebreaker: %v", err)
	}
}

func TestATotalIsOptInAndComesInPairs(t *testing.T) {
	opt := `service S { rpc ListThings (ListReq) returns (ListRes) { option (scorix.v1.list) = { default_page_size: 50 max_page_size: 200 order: "id" }; } }`
	both := strings.Replace(strings.Replace(listHead, "string query = 3;", "string query = 3; bool want_total = 4;", 1), "string next_page_token = 2;", "string next_page_token = 2; int32 total_size = 3;", 1)
	f, err := Parse("app.proto", both+opt)
	if err != nil || !f.Services[0].RPCs[0].List.Total {
		t.Fatalf("paired total: %v %+v", err, f.Services[0].RPCs[0].List)
	}
	if f, _ := Parse("app.proto", listHead+opt); f.Services[0].RPCs[0].List.Total {
		t.Error("a list with no want_total counts")
	}
	for name, c := range map[string]struct{ src, want string }{
		"want not bool": {strings.Replace(both, "bool want_total", "int32 want_total", 1), "want_total must be a bool"},
		"no total_size": {strings.Replace(both, " int32 total_size = 3;", "", 1), "has no int32 total_size"},
		"int64 total":   {strings.Replace(both, "int32 total_size", "int64 total_size", 1), "has no int32 total_size"},
		"unasked total": {strings.Replace(both, " bool want_total = 4;", "", 1), "counting is opt-in"},
	} {
		if _, err := Parse("app.proto", c.src+opt); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}
