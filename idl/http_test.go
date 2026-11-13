package idl

import (
	"context"
	"strings"
	"testing"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"

	"github.com/tradalab/scorix/idl/scorixpb"
)

const httpHead = `syntax = "proto3";
package acme;
import "scorix/v1/annotations.proto";
message Empty {}
message Req {
  string id = 1;
  int32 page = 2;
  string agent = 3 [(scorix.v1.field) = { header: "User-Agent" }];
  repeated string tags = 4;
}
`

func routes(t *testing.T, svc string) map[string]*HTTP {
	t.Helper()
	f, err := Parse("app.proto", httpHead+svc)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*HTTP{}
	for _, rpc := range f.Services[0].RPCs {
		out[rpc.Name] = rpc.HTTP
	}
	return out
}

func TestAnHTTPOptionBecomesTheRPCsRoute(t *testing.T) {
	r := routes(t, `service S {
  rpc Get (Req) returns (Empty) { option (scorix.v1.http) = { get: "/v1/things/{id}" }; }
  rpc Make (Req) returns (Empty) { option (scorix.v1.http) = { post: "/v1/things" body: "*" anonymous: true }; }
  rpc Plain (Req) returns (Empty);
}`)
	if g := r["Get"]; g == nil || g.Method != "GET" || g.Path != "/v1/things/{id}" || g.Body != "" || g.Anonymous || len(g.Params) != 1 || g.Params[0] != "id" {
		t.Errorf("Get route = %+v", g)
	}
	if m := r["Make"]; m == nil || m.Method != "POST" || m.Body != "*" || !m.Anonymous || len(m.Params) != 0 {
		t.Errorf("Make route = %+v", m)
	}
	if r["Plain"] != nil {
		t.Errorf("an rpc with no option got route %+v", r["Plain"])
	}
}

func TestAFieldOptionNamesTheHeaderItIsReadFrom(t *testing.T) {
	f, err := Parse("app.proto", httpHead+"service S { rpc Do (Req) returns (Empty); }")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range f.Messages {
		for _, fl := range m.Fields {
			if want := map[string]string{"agent": "User-Agent"}[fl.Name]; fl.Header != want {
				t.Errorf("%s.%s header = %q, want %q", m.Name, fl.Name, fl.Header, want)
			}
		}
	}
}

func refused(t *testing.T, name, src, want string) {
	t.Helper()
	if _, err := Parse("app.proto", src); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s: err = %v, want %q", name, err, want)
	}
}

func service(opt string) string {
	return "service S {\n  rpc Do (Req) returns (Empty) { option (scorix.v1.http) = { " + opt + " }; }\n  rpc Other (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/v1/other\" }; }\n}"
}

func TestRoutesThatCannotWorkAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ opt, want string }{
		"no method":      {`body: "*"`, "names no method"},
		"two methods":    {`get: "/a" post: "/b"`, "a route has one method"},
		"relative path":  {`get: "v1/x"`, "must start with /"},
		"odd body":       {`post: "/x" body: "id"`, `body "id"`},
		"get with body":  {`get: "/x" body: "*"`, "a GET has no body"},
		"unknown param":  {`get: "/x/{nope}"`, "{nope} is not a scalar field of Req"},
		"repeated param": {`get: "/x/{tags}"`, "{tags} is not a scalar field"},
		"same route":     {`get: "/v1/other"`, "both route GET /v1/other"},
	} {
		refused(t, name, httpHead+service(c.opt), c.want)
	}
}

func TestTwoRoutesThatDifferOnlyInParamNamesAreTheSameRoute(t *testing.T) {
	src := httpHead + "service S {\n  rpc A (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/{id}\" }; }\n  rpc B (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/{page}\" }; }\n}"
	refused(t, "param names", src, "both route GET /x/{}")
}

func TestOnePathNamesItsParamsTheSameWayUnderEveryMethod(t *testing.T) {
	src := httpHead + "service S {\n  rpc A (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/{id}\" }; }\n  rpc B (Req) returns (Empty) { option (scorix.v1.http) = { delete: \"/x/{page}\" }; }\n}"
	refused(t, "param names across methods", src, "S.B routes /x/{page} but S.A routes /x/{id}")
	same := httpHead + "service S {\n  rpc A (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/{id}\" }; }\n  rpc B (Req) returns (Empty) { option (scorix.v1.http) = { delete: \"/x/{id}\" }; }\n}"
	if _, err := Parse("app.proto", same); err != nil {
		t.Errorf("one path under two methods: %v", err)
	}
}

func TestAnEndAnchorMatchesOnlyThePathEndingInASlashAndIsNoParam(t *testing.T) {
	r := routes(t, `service S {
  rpc Index (Req) returns (Empty) { option (scorix.v1.http) = { get: "/v1/things/{$}" }; }
  rpc Get (Req) returns (Empty) { option (scorix.v1.http) = { get: "/v1/things/{id}" }; }
  rpc Under (Req) returns (Empty) { option (scorix.v1.http) = { get: "/v1/things/{id}/parts/{$}" }; }
}`)
	if g := r["Index"]; g == nil || g.Path != "/v1/things/{$}" || len(g.Params) != 0 {
		t.Errorf("Index route = %+v", g)
	}
	if g := r["Under"]; g == nil || g.Path != "/v1/things/{id}/parts/{$}" || len(g.Params) != 1 || g.Params[0] != "id" {
		t.Errorf("Under route = %+v", g)
	}
	for name, path := range map[string]string{"anchor inside": "/v1/{$}/things", "anchor glued": "/v1/things{$}", "two anchors": "/v1/{$}/{$}"} {
		refused(t, name, httpHead+service(`get: "`+path+`"`), "{$} only ends a path, after a /")
	}
}

func TestAnEndAnchorAndASubtreeOnOneMethodAreRefusedForSharingAnOpenAPIPath(t *testing.T) {
	src := httpHead + "service S {\n  rpc Index (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/{$}\" }; }\n  rpc Tree (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/\" }; }\n}"
	refused(t, "anchor and subtree", src, "S.Tree and S.Index both list as GET /x/ in OpenAPI")
	apart := httpHead + "service S {\n  rpc Index (Req) returns (Empty) { option (scorix.v1.http) = { get: \"/x/{$}\" }; }\n  rpc Tree (Req) returns (Empty) { option (scorix.v1.http) = { delete: \"/x/\" }; }\n}"
	if _, err := Parse("app.proto", apart); err != nil {
		t.Errorf("an anchor and a subtree under two methods: %v", err)
	}
}

func TestAHeaderBindsOnlyASingleString(t *testing.T) {
	src := strings.Replace(httpHead, `int32 page = 2;`, `int32 page = 2 [(scorix.v1.field) = { header: "X-Page" }];`, 1)
	refused(t, "int header", src+service(`get: "/x"`), `Req.page takes header "X-Page"`)
}

func TestAnAppProtoImportsOnlyTheScorixTrait(t *testing.T) {
	src := strings.Replace(httpHead, `import "scorix/v1/annotations.proto";`, `import "scorix/v1/annotations.proto";`+"\n"+`import "google/protobuf/timestamp.proto";`, 1)
	refused(t, "timestamp import", src+service(`get: "/x"`), "imports google/protobuf/timestamp.proto; an app proto may import only scorix/v1/annotations.proto")
}

func TestTheEmbeddedTraitIsTheOneScorixpbWasGeneratedFrom(t *testing.T) {
	comp := protocompile.Compiler{Resolver: protocompile.WithStandardImports(&protocompile.SourceResolver{
		Accessor: protocompile.SourceAccessorFromMap(map[string]string{traitPath: traitSource}),
	})}
	files, err := comp.Compile(context.Background(), traitPath)
	if err != nil {
		t.Fatal(err)
	}
	embedded := protodesc.ToFileDescriptorProto(files[0])
	generated := protodesc.ToFileDescriptorProto(scorixpb.File_scorix_v1_annotations_proto)
	embedded.SourceCodeInfo, generated.SourceCodeInfo = nil, nil
	if !proto.Equal(embedded, generated) {
		t.Fatal("idl/scorix/v1/annotations.proto changed without regenerating idl/scorixpb")
	}
}

func TestAFieldsClassIsRead(t *testing.T) {
	src := strings.Replace(httpHead, `string id = 1;`, `string id = 1 [(scorix.v1.field) = { class: SECRET }];`, 1)
	src = strings.Replace(src, `int32 page = 2;`, `int32 page = 2 [(scorix.v1.field) = { class: PII }];`, 1)
	f, err := Parse("app.proto", src+"service S { rpc Do (Req) returns (Empty); }")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range f.Messages {
		for _, fl := range m.Fields {
			if want := map[string]string{"id": "secret", "page": "pii"}[fl.Name]; m.Name == "Req" && fl.Class != want {
				t.Errorf("Req.%s class = %q, want %q", fl.Name, fl.Class, want)
			}
		}
	}
}

func TestTwoRoutesThatListAsOneOpenAPIPathUnderDifferentParamNamesAreRefused(t *testing.T) {
	_, err := Parse("app.proto", `syntax = "proto3";
package acme;
import "scorix/v1/annotations.proto";
message Req { string id = 1; string page = 2; }
service S {
  rpc A (Req) returns (Req) { option (scorix.v1.http) = { get: "/a/{id}/{$}" }; }
  rpc B (Req) returns (Req) { option (scorix.v1.http) = { post: "/a/{page}/" body: "*" }; }
}
`)
	if err == nil {
		t.Fatal("two routes naming one OpenAPI path's parameter differently were accepted")
	}
}
