package runner

import (
	"context"
	"encoding/json"
	"github.com/tradalab/scorix/idl"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const openAPIProto = `syntax = "proto3";
package acme.links;
import "scorix/v1/annotations.proto";
message Tag { string name = 1; }
message Link { string link_id = 1; int64 views = 2; repeated Tag tags = 3; }
message Unused { string x = 1; }
message GetReq { string link_id = 1; int32 page = 2; string agent = 3 [(scorix.v1.field) = { header: "User-Agent" }]; }
message PutReq { string link_id = 1; Link link = 2; }
service Links {
  // Look a link up.
  rpc Get (GetReq) returns (Link) { option (scorix.v1.http) = { get: "/v1/links/{link_id}" anonymous: true }; }
  rpc Put (PutReq) returns (Link) { option (scorix.v1.http) = { put: "/v1/links/{link_id}" body: "*" }; }
  rpc Hidden (GetReq) returns (Link);
}
`

func genOpenAPI(t *testing.T, proto, version string) map[string]any {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "links.proto")
	os.WriteFile(p, []byte(proto), 0o600)
	out := filepath.Join(dir, "api", "links.json")
	if err := GenerateOpenAPI(context.Background(), GenerateOpenAPIOptions{Proto: p, Out: out, Version: version}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("not JSON: %s", raw)
	}
	return doc
}

func TestOpenAPIDescribesEachRouteFromItsTraits(t *testing.T) {
	doc := genOpenAPI(t, openAPIProto, "2")
	if doc["openapi"] != "3.1.0" || doc["info"].(map[string]any)["title"] != "acme.links" || doc["info"].(map[string]any)["version"] != "2" {
		t.Fatalf("header = %v %v", doc["openapi"], doc["info"])
	}
	paths := doc["paths"].(map[string]any)
	get := paths["/v1/links/{link_id}"].(map[string]any)["get"].(map[string]any)
	params, _ := json.Marshal(get["parameters"])
	for _, want := range []string{`{"in":"path","name":"link_id","required":true`, `{"in":"query","name":"page"`, `{"in":"header","name":"User-Agent"`} {
		if !strings.Contains(string(params), want) {
			t.Errorf("GET parameters %s lack %s", params, want)
		}
	}
	if get["operationId"] != "Links_Get" || get["description"] != "Look a link up." || get["requestBody"] != nil {
		t.Errorf("GET = %v", get)
	}
	if sec, ok := get["security"].([]any); !ok || len(sec) != 0 {
		t.Errorf("an anonymous route kept the bearer requirement: %v", get["security"])
	}
	put := paths["/v1/links/{link_id}"].(map[string]any)["put"].(map[string]any)
	if put["requestBody"] == nil || put["security"] != nil {
		t.Errorf("PUT = %v", put)
	}
	if strings.Contains(string(mustJSON(t, put["parameters"])), `"in":"query"`) {
		t.Errorf("a body route took query parameters: %v", put["parameters"])
	}
	if len(paths) != 1 {
		t.Errorf("an rpc with no route got a path: %v", paths)
	}
	comps := doc["components"].(map[string]any)
	schemas := comps["schemas"].(map[string]any)
	link, _ := json.Marshal(schemas["Link"])
	if !strings.Contains(string(link), `"linkId":{"type":"string"}`) || !strings.Contains(string(link), `"tags":{"items":{"$ref":"#/components/schemas/Tag"},"type":"array"}`) {
		t.Errorf("Link schema = %s", link)
	}
	if schemas["Tag"] == nil || schemas["PutReq"] == nil || schemas["Unused"] != nil || schemas["GetReq"] != nil {
		t.Errorf("schemas %v, want only the ones a route reaches", keys(schemas))
	}
	if comps["securitySchemes"] == nil || doc["security"] == nil {
		t.Error("a document with a signed-in route declares no bearer scheme")
	}
}

func TestOpenAPIOfAnAllAnonymousServiceDeclaresNoBearer(t *testing.T) {
	doc := genOpenAPI(t, strings.Replace(openAPIProto, `body: "*" }`, `body: "*" anonymous: true }`, 1), "")
	if doc["info"].(map[string]any)["version"] != "0" {
		t.Errorf("an unversioned document got %v", doc["info"])
	}
	if doc["security"] != nil || doc["components"].(map[string]any)["securitySchemes"] != nil {
		t.Fatalf("security = %v", doc["security"])
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAnOptionalFieldMayBeNullInTheOpenAPIAndMCPSchemas(t *testing.T) {
	proto := strings.Replace(openAPIProto, "repeated Tag tags = 3; }", "repeated Tag tags = 3; optional string nick = 4; optional Tag best = 5; }", 1)
	link := string(mustJSON(t, genOpenAPI(t, proto, "1")["components"].(map[string]any)["schemas"].(map[string]any)["Link"]))
	for _, want := range []string{`"nick":{"anyOf":[{"type":"string"},{"type":"null"}]}`, `"best":{"anyOf":[{"$ref":"#/components/schemas/Tag"},{"type":"null"}]}`, `"linkId":{"type":"string"}`} {
		if !strings.Contains(link, want) {
			t.Errorf("Link schema %s lacks %s", link, want)
		}
	}
	pf, err := parseProto(strings.Replace(mcpSchemaProto, "Node tree = 8;", "Node tree = 8; optional string label = 9; optional Filter only = 10;", 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := enrichProto(&pf); err != nil {
		t.Fatal(err)
	}
	schema := pf.Services[0].RPCs[0].MCPSchema
	for _, want := range []string{`"label":{"anyOf":[{"type":"string"},{"type":"null"}]}`, `"only":{"anyOf":[{"additionalProperties":false`, `"connection_id":{"type":"string"}`} {
		if !strings.Contains(schema, want) {
			t.Errorf("MCP schema %s lacks %s", schema, want)
		}
	}
}

const enumMapProto = `syntax = "proto3";
package acme.notify;
import "scorix/v1/annotations.proto";
enum Priority {
  PRIORITY_UNSPECIFIED = 0;
  HIGH = 1;
}
message Tag { string name = 1; }
message Ev {
  Priority priority = 1;
  map<string, string> data = 2;
  map<string, Tag> tags = 3;
  repeated Priority seen = 4;
  map<int64, string> by_num = 5;
  map<fixed32, string> by_unsigned = 6;
  map<bool, string> by_flag = 7;
  map<sint32, Priority> ranks = 8;
}
service Notify {
  rpc Send (Ev) returns (Ev) { option (scorix.v1.http) = { post: "/v1/send" body: "*" }; }
}
`

const priorityJSON = `{"anyOf":[{"enum":["PRIORITY_UNSPECIFIED","HIGH"],"type":"string"},{"type":"integer"}]}`

func TestAnEnumIsANameOrANumberAndAMapAnObjectInTheOpenAPIAndMCPSchemas(t *testing.T) {
	ev := string(mustJSON(t, genOpenAPI(t, enumMapProto, "1")["components"].(map[string]any)["schemas"].(map[string]any)["Ev"]))
	for _, want := range []string{
		`"priority":` + priorityJSON,
		`"data":{"additionalProperties":{"type":"string"},"type":"object"}`,
		`"tags":{"additionalProperties":{"$ref":"#/components/schemas/Tag"},"type":"object"}`,
		`"seen":{"items":` + priorityJSON + `,"type":"array"}`,
		`"byNum":{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^-?[0-9]+$"},"type":"object"}`,
		`"byUnsigned":{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^[0-9]+$"},"type":"object"}`,
		`"byFlag":{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^(true|false)$"},"type":"object"}`,
		`"ranks":{"additionalProperties":` + priorityJSON + `,"propertyNames":{"pattern":"^-?[0-9]+$"},"type":"object"}`,
	} {
		if !strings.Contains(ev, want) {
			t.Errorf("Ev schema %s lacks %s", ev, want)
		}
	}
	pf, err := idl.Parse("notify.proto", enumMapProto)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := schemaOf(pf, "Ev", true, func(f protoField) string { return f.WireName })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"priority":` + priorityJSON,
		`"data":{"additionalProperties":{"type":"string"},"type":"object"}`,
		`"tags":{"additionalProperties":{"additionalProperties":false,"properties":{"name":{"type":"string"}},"type":"object"},"type":"object"}`,
		`"byNum":{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^-?[0-9]+$"},"type":"object"}`,
		`"byFlag":{"additionalProperties":{"type":"string"},"propertyNames":{"pattern":"^(true|false)$"},"type":"object"}`,
	} {
		if !strings.Contains(schema, want) {
			t.Errorf("MCP schema %s lacks %s", schema, want)
		}
	}
}

func TestAMapFieldIsNoQueryParameterSinceRESTIgnoresIt(t *testing.T) {
	proto := strings.Replace(openAPIProto, "string agent = 3 [(scorix.v1.field) = { header: \"User-Agent\" }]; }", "string agent = 3 [(scorix.v1.field) = { header: \"User-Agent\" }]; map<string, string> tags = 4; }", 1)
	get := genOpenAPI(t, proto, "1")["paths"].(map[string]any)["/v1/links/{link_id}"].(map[string]any)["get"].(map[string]any)
	params := string(mustJSON(t, get["parameters"]))
	if strings.Contains(params, `"name":"tags"`) || !strings.Contains(params, `"name":"page"`) {
		t.Fatalf("GET parameters = %s", params)
	}
}

func TestAnEndAnchorIsThePathEndingInASlashAndNoParameterInTheOpenAPI(t *testing.T) {
	proto := strings.Replace(openAPIProto, "rpc Hidden (GetReq) returns (Link);", `rpc Index (GetReq) returns (Link) { option (scorix.v1.http) = { get: "/v1/links/{$}" }; }`, 1)
	paths := genOpenAPI(t, proto, "1")["paths"].(map[string]any)
	index, ok := paths["/v1/links/"].(map[string]any)
	if !ok {
		t.Fatalf("paths = %v", keys(paths))
	}
	if params := string(mustJSON(t, index["get"].(map[string]any)["parameters"])); strings.Contains(params, `"in":"path"`) || strings.Contains(params, "$") {
		t.Errorf("GET /v1/links/ parameters = %s", params)
	}
}

func TestAnOptionalFieldIsAPlainParameterSinceAParameterCannotBeNull(t *testing.T) {
	proto := strings.NewReplacer(
		"message GetReq { string link_id = 1; int32 page = 2; string agent = 3", "message GetReq { optional string link_id = 1; optional int32 page = 2; optional string agent = 3",
		"message Link { string link_id = 1;", "message Link { optional string link_id = 1;",
	).Replace(openAPIProto)
	doc := genOpenAPI(t, proto, "1")
	get := doc["paths"].(map[string]any)["/v1/links/{link_id}"].(map[string]any)["get"].(map[string]any)
	params := string(mustJSON(t, get["parameters"]))
	for _, want := range []string{`"in":"path","name":"link_id","required":true,"schema":{"type":"string"}`, `"in":"query","name":"page","schema":{"type":"integer"}`, `"in":"header","name":"User-Agent","schema":{"type":"string"}`} {
		if !strings.Contains(params, want) {
			t.Errorf("GET parameters %s lack %s", params, want)
		}
	}
	link := string(mustJSON(t, doc["components"].(map[string]any)["schemas"].(map[string]any)["Link"]))
	if !strings.Contains(link, `"linkId":{"anyOf":[{"type":"string"},{"type":"null"}]}`) {
		t.Errorf("the Link body schema lost its null: %s", link)
	}
}

func TestAMapKeyIsTheStringOfItsKeyType(t *testing.T) {
	for kinds, want := range map[string]string{
		"int32 sint32 sfixed32 int64 sint64 sfixed64": `{"pattern":"^-?[0-9]+$"}`,
		"uint32 fixed32 uint64 fixed64":               `{"pattern":"^[0-9]+$"}`,
		"bool":                                        `{"pattern":"^(true|false)$"}`,
		"string":                                      `null`,
	} {
		for _, k := range strings.Fields(kinds) {
			s := shaped(protoField{Map: true, MapKey: k}, map[string]any{"type": "string"})
			if got := string(mustJSON(t, s["propertyNames"])); got != want {
				t.Errorf("map<%s, string> propertyNames = %s, want %s", k, got, want)
			}
		}
	}
}
