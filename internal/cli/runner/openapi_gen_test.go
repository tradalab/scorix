package runner

import (
	"context"
	"encoding/json"
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
