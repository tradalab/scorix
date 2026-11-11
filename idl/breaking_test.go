package idl

import (
	"strings"
	"testing"
)

const breakHead = `syntax = "proto3";
package acme.v1;
import "scorix/v1/annotations.proto";
message Empty {}
`

const breakBase = breakHead + `message Req {
  string id = 1;
  int32 count = 2;
  string agent = 3 [(scorix.v1.field) = { header: "User-Agent" }];
  bytes blob = 4;
  sint32 delta = 5;
  Empty inner = 6;
  int32 page_size = 7;
  string page_token = 8;
  repeated string words = 11;
  string token = 12;
}
message Page {
  repeated Empty items = 1;
  string next_page_token = 2;
}
service Links {
  rpc GetLink (Req) returns (Empty) { option (scorix.v1.http) = { get: "/v1/links/{id}" anonymous: true }; }
  rpc SaveLink (Req) returns (Empty) { option (scorix.v1.http) = { post: "/v1/links" body: "*" }; option (scorix.v1.rpc) = { effect: IDEMPOTENT permission: "links.write" replay: true }; }
  rpc ListLinks (Req) returns (Page) { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 50 order: "created_at desc, id desc" }; }
  // Find a link.
  // @mcp
  rpc FindLink (Req) returns (Empty);
  // Count links.
  // @cli
  rpc CountLinks (Req) returns (Empty);
  rpc Upload (stream Req) returns (Empty);
  rpc Peek (Note) returns (Empty);
  rpc Typed (Req) returns (Empty) { option (scorix.v1.event) = { direction: IN }; }
  rpc Archive (Req) returns (Empty) { option (scorix.v1.cli) = { arg: "id" rest: "words" secret: "token" }; }
  rpc AllLinks (Req) returns (Page);
}
message Note {
  string by = 1;
}
`

func breaks(t *testing.T, after string) []Change {
	t.Helper()
	changes, err := Breaking("app.proto", breakBase, after)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func edit(t *testing.T, from, to string) string {
	t.Helper()
	if !strings.Contains(breakBase, from) {
		t.Fatalf("the base proto has no %q", from)
	}
	return strings.Replace(breakBase, from, to, 1)
}

func TestTheSameContractBreaksNothing(t *testing.T) {
	if c := breaks(t, breakBase); len(c) != 0 {
		t.Fatalf("an unchanged proto broke %v", c)
	}
}

func TestWhatACallerCannotNoticeBreaksNothing(t *testing.T) {
	for name, after := range map[string]string{
		"new field":          edit(t, "  string page_token = 8;\n", "  string page_token = 8;\n  string note = 9;\n"),
		"new rpc":            edit(t, "service Links {\n", "service Links {\n  rpc Ping (Empty) returns (Empty);\n"),
		"new message":        breakBase + "message Unused { string x = 1; }\n",
		"safer effect":       edit(t, "  rpc FindLink (Req) returns (Empty);", "  rpc FindLink (Req) returns (Empty) { option (scorix.v1.rpc) = { effect: IDEMPOTENT }; }"),
		"same permission":    edit(t, `{ effect: IDEMPOTENT permission: "links.write" replay: true }`, `{ replay: true permission: "links.write" effect: IDEMPOTENT }`),
		"anonymous added":    edit(t, `{ post: "/v1/links" body: "*" }`, `{ post: "/v1/links" body: "*" anonymous: true }`),
		"param renamed":      strings.Replace(edit(t, `"/v1/links/{id}"`, `"/v1/links/{key}"`), "  string id = 1;\n", "  string id = 1;\n  string key = 10;\n", 1),
		"permission dropped": edit(t, ` permission: "links.write"`, ""),
		"header dropped from a type no rpc reads": breakBase + "message Memo { string by = 1 [(scorix.v1.field) = { header: \"X-By\" }]; }\n",
		"header for a new route":                  strings.Replace(edit(t, "  rpc Peek (Note) returns (Empty);", `  rpc Peek (Note) returns (Empty) { option (scorix.v1.http) = { get: "/v1/peek" }; }`), "  string by = 1;", `  string by = 1 [(scorix.v1.field) = { header: "X-By" }];`, 1),
		"bigger page":                             edit(t, "max_page_size: 50", "max_page_size: 100"),
		"comment":                                 edit(t, "// Find a link.", "// Find one link by its id."),
	} {
		if c := breaks(t, after); len(c) != 0 {
			t.Errorf("%s broke %v", name, c)
		}
	}
}

func TestEachBreakIsReportedAtTheLevelThatFirstNoticesIt(t *testing.T) {
	for name, c := range map[string]struct {
		after string
		level Level
		want  string
	}{
		"package":                    {strings.Replace(breakBase, "package acme.v1;", "package acme.v2;", 1), Wire, "package acme.v1 is acme.v2 now"},
		"service removed":            {strings.Replace(breakBase, "service Links {", "service Links2 {", 1), Wire, "service Links was removed"},
		"rpc removed":                {edit(t, "  rpc FindLink (Req) returns (Empty);\n", ""), Wire, "rpc Links.FindLink was removed"},
		"request type":               {edit(t, "rpc FindLink (Req)", "rpc FindLink (Empty)"), Wire, "takes Empty, was Req"},
		"response type":              {edit(t, "rpc FindLink (Req) returns (Empty)", "rpc FindLink (Req) returns (Page)"), Wire, "returns Page, was Empty"},
		"stream":                     {edit(t, "rpc FindLink (Req) returns (Empty)", "rpc FindLink (Req) returns (stream Empty)"), Wire, "streams its response now"},
		"field dropped":              {edit(t, "  int32 count = 2;\n", ""), Wire, "field 2 (count) was removed without reserving 2"},
		"field type":                 {edit(t, "  int32 count = 2;", "  string count = 2;"), Wire, "field 2 (count) is string, was int32"},
		"field message type":         {edit(t, "  Empty inner = 6;", "  Page inner = 6;"), Wire, "field 6 (inner) is Page, was Empty"},
		"zigzag":                     {edit(t, "  sint32 delta = 5;", "  int32 delta = 5;"), Wire, "field 5 (delta) is int32, was sint32"},
		"repeated":                   {edit(t, "  int32 count = 2;", "  repeated int32 count = 2;"), Wire, "field 2 (count) is repeated now"},
		"field reserved":             {edit(t, "  int32 count = 2;\n", "  reserved 2;\n"), JSON, "field 2 (count) was removed"},
		"field renamed":              {edit(t, "  int32 count = 2;", "  int32 total = 2;"), JSON, "field 2 was renamed count to total"},
		"json name":                  {edit(t, "  int32 count = 2;", `  int32 count = 2 [json_name = "n"];`), JSON, "field 2 (count) is n in JSON, was count"},
		"widened int":                {edit(t, "  int32 count = 2;", "  int64 count = 2;"), JSON, "field 2 (count) is int64, was int32"},
		"bytes to string":            {edit(t, "  bytes blob = 4;", "  string blob = 4;"), JSON, "field 4 (blob) is string, was bytes"},
		"route removed":              {edit(t, ` { option (scorix.v1.http) = { get: "/v1/links/{id}" anonymous: true }; }`, ";"), Contract, "REST route GET /v1/links/{id} was removed"},
		"route moved":                {edit(t, `get: "/v1/links/{id}"`, `get: "/v1/link/{id}"`), Contract, "REST route GET /v1/links/{id} is GET /v1/link/{id} now"},
		"route method":               {edit(t, `post: "/v1/links"`, `put: "/v1/links"`), Contract, "REST route POST /v1/links is PUT /v1/links now"},
		"body":                       {edit(t, `post: "/v1/links" body: "*"`, `post: "/v1/links"`), Contract, "reads the query now, was the body"},
		"anonymous":                  {edit(t, `get: "/v1/links/{id}" anonymous: true`, `get: "/v1/links/{id}"`), Contract, "needs a signed-in caller now"},
		"header":                     {edit(t, `string agent = 3 [(scorix.v1.field) = { header: "User-Agent" }];`, "string agent = 3;"), Contract, "field agent no longer comes from header User-Agent"},
		"riskier effect":             {edit(t, "effect: IDEMPOTENT ", "effect: MUTATING "), Contract, "is MUTATING now, was IDEMPOTENT"},
		"named effect lost":          {edit(t, `anonymous: true }; }`, `anonymous: true }; option (scorix.v1.rpc) = { effect: MUTATING }; }`), Contract, "is MUTATING now, was READONLY"},
		"permission added":           {edit(t, "  rpc FindLink (Req) returns (Empty);", `  rpc FindLink (Req) returns (Empty) { option (scorix.v1.rpc) = { permission: "links.read" }; }`), Contract, "needs permission links.read now"},
		"permission changed":         {edit(t, `permission: "links.write"`, `permission: "links.admin"`), Contract, "needs permission links.admin now, was links.write"},
		"replay dropped":             {edit(t, " replay: true", ""), Contract, "no longer replays a retry that carries its Idempotency-Key"},
		"audit dropped":              {edit(t, " replay: true", " replay: true audit: false"), Contract, "is no longer audited"},
		"paging dropped":             {edit(t, ` { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 50 order: "created_at desc, id desc" }; }`, ";"), Contract, "no longer pages"},
		"order":                      {edit(t, `order: "created_at desc, id desc"`, `order: "created_at asc, id asc"`), Contract, "orders by created_at asc, id asc now, was created_at desc, id desc"},
		"cli dropped":                {edit(t, "  // @cli\n", ""), Contract, "is no longer a CLI command"},
		"client stream":              {edit(t, "rpc Upload (stream Req)", "rpc Upload (Req)"), Wire, "no longer streams its request"},
		"header moved out of a body": {edit(t, "  string id = 1;", `  string id = 1 [(scorix.v1.field) = { header: "X-Id" }];`), Contract, "rpc Links.SaveLink field id comes from header X-Id now"},
		"header added":               {edit(t, "  string id = 1;", `  string id = 1 [(scorix.v1.field) = { header: "X-Id" }];`), Contract, "field id comes from header X-Id now"},
		"header moved":               {edit(t, `header: "User-Agent"`, `header: "X-Agent"`), Contract, "field agent comes from header X-Agent now, was User-Agent"},
		"event direction":            {edit(t, "direction: IN", "direction: OUT"), Contract, "rpc Links.Typed is an out event now, was in"},
		"event dropped":              {edit(t, " { option (scorix.v1.event) = { direction: IN }; }", ";"), Contract, "rpc Links.Typed is no longer an event"},
		"event added":                {edit(t, "  rpc FindLink (Req) returns (Empty);", "  rpc FindLink (Req) returns (Empty) { option (scorix.v1.event) = {}; }"), Contract, "rpc Links.FindLink is an out event now, was a call"},
		"cli arg":                    {edit(t, `arg: "id"`, `arg: "agent"`), Contract, "rpc Links.Archive takes field agent as its CLI argument now, was id"},
		"cli rest":                   {edit(t, ` rest: "words"`, ""), Contract, "rpc Links.Archive takes no field as the words after -- now, was words"},
		"cli secret":                 {edit(t, `secret: "token"`, `secret: "agent"`), Contract, "rpc Links.Archive takes field agent as its secret from stdin now, was token"},
		"paging added":               {edit(t, "  rpc AllLinks (Req) returns (Page);", `  rpc AllLinks (Req) returns (Page) { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 50 order: "id" }; }`), Contract, "rpc Links.AllLinks pages now"},
		"smaller page":               {edit(t, "max_page_size: 50", "max_page_size: 20"), Contract, "rpc Links.ListLinks returns at most 20 rows a page now, was 50"},
		"renamed same shape":         {strings.NewReplacer("rpc Peek (Note)", "rpc Peek (Memo)", "message Note {", "message Memo {").Replace(breakBase), JSON, "rpc Links.Peek takes Memo, was Note"},
		"renamed other kind":         {strings.NewReplacer("rpc Peek (Note)", "rpc Peek (Memo)", "message Note {\n  string by = 1;", "message Memo {\n  int32 by = 1;").Replace(breakBase), Wire, "rpc Links.Peek takes Memo, was Note"},
		"renamed other repeated":     {strings.NewReplacer("rpc Peek (Note)", "rpc Peek (Memo)", "message Note {\n  string by = 1;", "message Memo {\n  repeated string by = 1;").Replace(breakBase), Wire, "rpc Links.Peek takes Memo, was Note"},
		"renamed other number":       {strings.NewReplacer("rpc Peek (Note)", "rpc Peek (Memo)", "message Note {\n  string by = 1;", "message Memo {\n  string by = 2;").Replace(breakBase), Wire, "rpc Links.Peek takes Memo, was Note"},
		"mcp dropped":                {edit(t, "  // @mcp\n", ""), Contract, "is no longer an MCP tool"},
	} {
		got := breaks(t, c.after)
		found := false
		for _, ch := range got {
			if ch.Level == c.level && strings.Contains(ch.String(), c.want) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want %s %q in %v", name, c.level, c.want, got)
		}
	}
}

func TestARenamedPackageIsOneChangeNotOneForEveryTypeInIt(t *testing.T) {
	if c := breaks(t, strings.Replace(breakBase, "package acme.v1;", "package acme.v2;", 1)); len(c) != 1 {
		t.Fatalf("a renamed package = %v", c)
	}
}

func TestAProtoThatDoesNotCompileIsAnErrorNamingWhichSide(t *testing.T) {
	if _, err := Breaking("app.proto", "syntax = \"proto3\"; message {", breakBase); err == nil || !strings.HasPrefix(err.Error(), "before: ") {
		t.Errorf("a broken old proto = %v", err)
	}
	if _, err := Breaking("app.proto", breakBase, "nope"); err == nil || !strings.HasPrefix(err.Error(), "after: ") {
		t.Errorf("a broken new proto = %v", err)
	}
}
