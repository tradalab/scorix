package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const serverProto = `syntax = "proto3";
package acme.links;
message Req { string id = 1; }
message Res { string url = 1; }
service Links {
  rpc Get (Req) returns (Res);
  rpc Put (Req) returns (Res);
}
`

func genServer(t *testing.T, proto string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "links.proto")
	if err := os.WriteFile(p, []byte(proto), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "linkserver", "routes.go")
	err := GenerateServer(context.Background(), GenerateServerOptions{Proto: p, PB: "example.com/acme/pb", Out: out})
	b, _ := os.ReadFile(out)
	return string(b), err
}

func TestGenerateServerRoutesEveryRPCUnderItsFullProcedure(t *testing.T) {
	src, err := genServer(t, serverProto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"package linkserver",
		`pb "example.com/acme/pb"`,
		`const LinksPath = "/acme.links.Links/"`,
		"Get(context.Context, *pb.Req) (*pb.Res, error)",
		`connect.Unary(m, LinksPath+"Get", kernel.Rule{}, h.Get)`,
		`connect.Unary(m, LinksPath+"Put", kernel.Rule{Audit: true}, h.Put)`,
		"func NewLinksClient(c *connect.Conn) LinksHandler { return linksClient{c} }",
		`return connect.Call[pb.Req, pb.Res](ctx, x.c, LinksPath+"Get", in)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
}

func TestGenerateServerRefusesWhatItCannotServe(t *testing.T) {
	for name, c := range map[string]struct{ proto, want string }{
		"event":      {strings.Replace(serverProto, "  rpc Put", "  // @event\n  rpc Put", 1), "Links.Put is an event"},
		"no package": {strings.Replace(serverProto, "package acme.links;\n", "", 1), "has no package"},
	} {
		if _, err := genServer(t, c.proto); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestGenerateServerCheckReportsAFileThatDrifted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "links.proto")
	os.WriteFile(p, []byte(serverProto), 0o600)
	out := filepath.Join(dir, "srv", "routes.go")
	opt := GenerateServerOptions{Proto: p, PB: "example.com/acme/pb", Out: out}
	if err := GenerateServer(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	opt.Check = true
	if err := GenerateServer(context.Background(), opt); err != nil {
		t.Fatalf("check on a fresh file = %v", err)
	}
	os.WriteFile(p, []byte(strings.Replace(serverProto, "rpc Put (Req) returns (Res);", "rpc Put (Req) returns (Res);\n  rpc Del (Req) returns (Res);", 1)), 0o600)
	if err := GenerateServer(context.Background(), opt); err == nil {
		t.Fatal("check passed although the proto gained an rpc the file does not route")
	}
}

func TestGenerateServerNamesTheFlagsItNeeds(t *testing.T) {
	err := GenerateServer(context.Background(), GenerateServerOptions{Proto: "x.proto", Out: "srv/routes.go"})
	if err == nil || !strings.Contains(err.Error(), "--pb") {
		t.Fatalf("err = %v, want the missing --pb named", err)
	}
}

const httpServerProto = `syntax = "proto3";
package acme.links;
import "scorix/v1/annotations.proto";
message Req {
  string id = 1;
  string agent = 2 [(scorix.v1.field) = { header: "User-Agent" }];
}
message Res { string url = 1; }
service Links {
  rpc Get (Req) returns (Res) { option (scorix.v1.http) = { get: "/v1/links/{id}" anonymous: true }; }
  rpc Put (Req) returns (Res) {
    option (scorix.v1.http) = { put: "/v1/links/{id}" body: "*" };
    option (scorix.v1.rpc) = { timeout: "3s" permission: "links.write" };
  }
  rpc Hidden (Req) returns (Res);
}
`

func TestGenerateServerRoutesTheRPCsThatHaveAnHTTPOption(t *testing.T) {
	src, err := genServer(t, httpServerProto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github.com/tradalab/scorix/transport/rest"`,
		"func RegisterLinksHTTP(m *rest.Mux, h LinksHandler) {",
		`rest.Handle(m, rest.Route{Method: "GET", Path: "/v1/links/{id}", Procedure: LinksPath + "Get", Anonymous: true, Headers: map[string]string{"agent": "User-Agent"}, Rule: kernel.Rule{}}, h.Get)`,
		`rest.Handle(m, rest.Route{Method: "PUT", Path: "/v1/links/{id}", Procedure: LinksPath + "Put", Body: "*", Headers: map[string]string{"agent": "User-Agent"}, Rule: kernel.Rule{Timeout: 3000000000, Permission: "links.write", Audit: true}}, h.Put)`,
		`connect.Unary(m, LinksPath+"Put", kernel.Rule{Timeout: 3000000000, Permission: "links.write", Audit: true}, h.Put)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, `"Hidden"}, h.Hidden)`) {
		t.Error("an rpc without an HTTP option got a route")
	}
	plain, err := genServer(t, serverProto)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "transport/rest") || strings.Contains(plain, "HTTP(") {
		t.Errorf("a proto with no HTTP option imports rest or registers routes:\n%s", plain)
	}
}

const mcpServerProto = `syntax = "proto3";
package acme.links;
import "scorix/v1/annotations.proto";
message Req { string link_id = 1; int64 views = 2; }
message Res { string url = 1; }
service Links {
  // Look up where a short link points.
  // @mcp
  rpc Get (Req) returns (Res);
  // Remove a short link for good.
  // @mcp destructive
  rpc Drop (Req) returns (Res) { option (scorix.v1.rpc) = { permission: "links.admin" }; }
  // Pin a short link; pinning twice is pinning once.
  // @mcp
  rpc Pin (Req) returns (Res) { option (scorix.v1.rpc) = { effect: IDEMPOTENT }; }
  rpc Quiet (Req) returns (Res);
}
`

func TestGenerateServerExposesTheMCPRPCsAsTools(t *testing.T) {
	src, err := genServer(t, mcpServerProto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github.com/tradalab/scorix/transport/mcp"`,
		"func RegisterLinksMCP(m *mcp.Mux, h LinksHandler) {",
		`Name:         "links_get",`,
		`Description:  "Look up where a short link points.",`,
		`Procedure:    LinksPath + "Get",`,
		`InputSchema:  json.RawMessage("{\"additionalProperties\":false,\"properties\":{\"linkId\":{\"type\":\"string\"},\"views\":{\"type\":\"integer\"}},\"type\":\"object\"}"),`,
		`OutputSchema: json.RawMessage("{\"properties\":{\"url\":{\"type\":\"string\"}},\"type\":\"object\"}"),`,
		`Name:         "links_drop",`,
		"Destructive:  true,",
		"ReadOnly:     true,",
		"Idempotent:   true,",
		`Rule:         kernel.Rule{Permission: "links.admin", Audit: true},`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
	if strings.Count(src, "Destructive:") != 1 || strings.Count(src, "ReadOnly:") != 1 || strings.Count(src, "Idempotent:") != 1 || strings.Contains(src, "links_quiet") {
		t.Errorf("Quiet became a tool, or Get was marked destructive:\n%s", src)
	}
	plain, _ := genServer(t, serverProto)
	if strings.Contains(plain, "transport/mcp") || strings.Contains(plain, "encoding/json") {
		t.Errorf("a proto with no @mcp imports mcp:\n%s", plain)
	}
}

func TestGenerateServerRefusesAToolAnAgentCannotChoose(t *testing.T) {
	undocumented := strings.Replace(mcpServerProto, "  // Look up where a short link points.\n", "", 1)
	if _, err := genServer(t, undocumented); err == nil || !strings.Contains(err.Error(), "Links.Get is @mcp with no comment") {
		t.Errorf("undocumented tool: %v", err)
	}
	twice := strings.Replace(mcpServerProto, "service Links {", "service Links {\n  // Twin.\n  // @mcp\n  rpc GET (Req) returns (Res) { option (scorix.v1.rpc) = { effect: READONLY }; }", 1)
	if _, err := genServer(t, twice); err == nil || !strings.Contains(err.Error(), `both become the MCP tool "links_get"`) {
		t.Errorf("colliding tools: %v", err)
	}
	long := strings.Replace(mcpServerProto, "rpc Get (Req)", "rpc Get"+strings.Repeat("Thing", 20)+" (Req)", 1)
	effectless := strings.Replace(mcpServerProto, "rpc Get (Req)", "rpc Fetch (Req)", 1)
	if _, err := genServer(t, effectless); err == nil || !strings.Contains(err.Error(), "Links.Fetch is @mcp with no effect") {
		t.Errorf("tool with no effect: %v", err)
	}
	if _, err := genServer(t, long); err == nil || !strings.Contains(err.Error(), "tool names are 1-64") {
		t.Errorf("overlong tool name: %v", err)
	}
}

func TestGenerateServerGivesEachListItsPageSpec(t *testing.T) {
	proto := `syntax = "proto3";
package acme.links;
import "scorix/v1/annotations.proto";
message Link { string id = 1; }
message ListReq { int32 page_size = 1; string page_token = 2; }
message ListRes { repeated Link links = 1; string next_page_token = 2; }
service Links {
  rpc ListLinks (ListReq) returns (ListRes) { option (scorix.v1.list) = { default_page_size: 50 max_page_size: 200 order: "created_at desc, id desc" }; }
}
service Archive {
  rpc ListLinks (ListReq) returns (ListRes) { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 20 order: "id asc" }; }
}
`
	src, err := genServer(t, proto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github.com/tradalab/scorix/data/page"`,
		`var LinksListLinksPage = page.Spec{Default: 50, Max: 200, Order: []page.Order{{Column: "created_at", Desc: true}, {Column: "id", Desc: true}}}`,
		`var ArchiveListLinksPage = page.Spec{`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
	plain, _ := genServer(t, serverProto)
	if strings.Contains(plain, "data/page") {
		t.Error("a proto with no list imports page")
	}
}

func TestGenerateDispatchHandsEachRPCToItsLogic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "links.proto")
	os.WriteFile(p, []byte(serverProto), 0o600)
	out := filepath.Join(dir, "server", "dispatch.go")
	opt := GenerateDispatchOptions{Proto: p, PB: "example.com/acme/pb", Logic: "example.com/acme/rpc/internal/logic", Svc: "example.com/acme/rpc/internal/svc", Out: out}
	if err := GenerateDispatch(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(out)
	src := string(b)
	for _, want := range []string{
		"package server",
		`logic "example.com/acme/rpc/internal/logic"`,
		`svc "example.com/acme/rpc/internal/svc"`,
		"func NewLinksServer(svcCtx *svc.ServiceContext) *LinksServer {",
		"func (s *LinksServer) Get(ctx context.Context, in *pb.Req) (*pb.Res, error) {\n\treturn logic.NewGetLogic(ctx, s.svcCtx).Get(in)\n}",
		"return logic.NewPutLogic(ctx, s.svcCtx).Put(in)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("dispatch lacks %q:\n%s", want, src)
		}
	}
	opt.Logic = ""
	if err := GenerateDispatch(context.Background(), opt); err == nil || !strings.Contains(err.Error(), "--logic") {
		t.Errorf("missing --logic: %v", err)
	}
	opt.Logic = "x"
	os.WriteFile(p, []byte(strings.NewReplacer("rpc Get (Req)", "rpc Get (stream Req)", "rpc Put (Req) returns (Res)", "rpc Put (stream Req) returns (stream Res)").Replace(serverProto)), 0o600)
	if err := GenerateDispatch(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(opt.Out)
	for _, want := range []string{
		"func (s *LinksServer) Get(ctx context.Context, recv func() (*pb.Req, error)) (*pb.Res, error) {\n\treturn logic.NewGetLogic(ctx, s.svcCtx).Get(recv)",
		"func (s *LinksServer) Put(ctx context.Context, recv func() (*pb.Req, error), send func(*pb.Res) error) error {\n\treturn logic.NewPutLogic(ctx, s.svcCtx).Put(recv, send)",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("client-stream and bidi dispatch lack %q:\n%s", want, b)
		}
	}
	os.WriteFile(p, []byte(strings.Replace(serverProto, "rpc Get (Req) returns (Res);", "rpc Get (Req) returns (stream Res);", 1)), 0o600)
	if err := GenerateDispatch(context.Background(), opt); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(opt.Out); !strings.Contains(string(b), "func (s *LinksServer) Get(ctx context.Context, in *pb.Req, send func(*pb.Res) error) error {\n\treturn logic.NewGetLogic(ctx, s.svcCtx).Get(in, send)") {
		t.Errorf("stream dispatch:\n%s", b)
	}
}

func TestGenerateServerRefusesAToolWhoseRequestCarriesASecret(t *testing.T) {
	direct := strings.Replace(mcpServerProto, "message Req { string link_id = 1; int64 views = 2; }", "message Req { string link_id = 1; int64 views = 2; string token = 3 [(scorix.v1.field) = { class: SECRET }]; }", 1)
	if _, err := genServer(t, direct); err == nil || !strings.Contains(err.Error(), "carries the SECRET field Req.token") {
		t.Errorf("direct secret: %v", err)
	}
	nested := strings.Replace(mcpServerProto, "message Req { string link_id = 1; int64 views = 2; }", "message Cred { string key = 1 [(scorix.v1.field) = { class: SECRET }]; }\nmessage Req { string link_id = 1; int64 views = 2; Cred cred = 3; }", 1)
	if _, err := genServer(t, nested); err == nil || !strings.Contains(err.Error(), "carries the SECRET field Cred.key") {
		t.Errorf("nested secret: %v", err)
	}
	recursive := strings.Replace(mcpServerProto, "message Req { string link_id = 1; int64 views = 2; }", "message Node { Node next = 1; }\nmessage Req { string link_id = 1; int64 views = 2; Node chain = 3; }", 1)
	if _, err := genServer(t, recursive); err != nil {
		t.Errorf("a self-referencing request: %v", err)
	}
	pii := strings.Replace(mcpServerProto, "message Req { string link_id = 1; int64 views = 2; }", "message Req { string link_id = 1 [(scorix.v1.field) = { class: PII }]; int64 views = 2; }", 1)
	if _, err := genServer(t, pii); err != nil {
		t.Errorf("a PII argument is the person's own data and was refused: %v", err)
	}
}

func TestGenerateServerServesAServerStreamAndRefusesItWhereItCannotGo(t *testing.T) {
	stream := strings.Replace(serverProto, "rpc Get (Req) returns (Res);", "rpc Get (Req) returns (stream Res);", 1)
	src, err := genServer(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Get(context.Context, *pb.Req, func(*pb.Res) error) error",
		`connect.ServerStream(m, LinksPath+"Get", kernel.Rule{}, h.Get)`,
		"func (x linksClient) Get(ctx context.Context, in *pb.Req, each func(*pb.Res) error) error {",
		`return connect.Stream[pb.Req, pb.Res](ctx, x.c, LinksPath+"Get", in, each)`,
		`connect.Unary(m, LinksPath+"Put", kernel.Rule{Audit: true}, h.Put)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
	withImport := strings.Replace(stream, "package acme.links;\n", "package acme.links;\nimport \"scorix/v1/annotations.proto\";\n", 1)
	for name, c := range map[string]struct{ proto, want string }{
		"route": {strings.Replace(withImport, "returns (stream Res);", `returns (stream Res) { option (scorix.v1.http) = { get: "/x/{id}" }; }`, 1), "Links.Get is server-stream and has (scorix.v1.http); REST takes one request and answers once"},
		"list":  {strings.Replace(strings.NewReplacer("message Req { string id = 1; }", "message Req { string id = 1; int32 page_size = 2; string page_token = 3; }", "message Res { string url = 1; }", "message Res { repeated Req items = 1; string next_page_token = 2; }").Replace(withImport), "returns (stream Res);", `returns (stream Res) { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 20 order: "id asc" }; }`, 1), "Links.Get is server-stream and pages"},
		"tool":  {strings.Replace(withImport, "  rpc Get (Req) returns (stream Res);", "  // Gets.\n  rpc Get (Req) returns (stream Res) { option (scorix.v1.mcp) = {}; }", 1), "Links.Get is server-stream and an MCP tool; a tool call takes one input and answers once"},
	} {
		if _, err := genServer(t, c.proto); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestGenerateServerServesClientStreamsAndBidiAndRefusesThemWhereTheyCannotGo(t *testing.T) {
	duplex := strings.NewReplacer("rpc Get (Req)", "rpc Get (stream Req)", "rpc Put (Req) returns (Res)", "rpc Put (stream Req) returns (stream Res)").Replace(serverProto)
	src, err := genServer(t, duplex)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Get(context.Context, func() (*pb.Req, error)) (*pb.Res, error)",
		"Put(context.Context, func() (*pb.Req, error), func(*pb.Res) error) error",
		`connect.ClientStream(m, LinksPath+"Get", kernel.Rule{}, h.Get)`,
		`connect.BidiStream(m, LinksPath+"Put", kernel.Rule{Audit: true}, h.Put)`,
		`return connect.CallClientStream[pb.Req, pb.Res](ctx, x.c, LinksPath+"Get", recv)`,
		`return connect.CallBidiStream(ctx, x.c, LinksPath+"Put", recv, send)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
	withImport := strings.Replace(duplex, "package acme.links;\n", "package acme.links;\nimport \"scorix/v1/annotations.proto\";\n", 1)
	routed := strings.Replace(withImport, "rpc Get (stream Req) returns (Res);", `rpc Get (stream Req) returns (Res) { option (scorix.v1.http) = { post: "/x" body: "*" }; }`, 1)
	if _, err := genServer(t, routed); err == nil || !strings.Contains(err.Error(), "Links.Get is client-stream and has (scorix.v1.http)") {
		t.Errorf("a routed client stream: %v", err)
	}
	tool := strings.Replace(withImport, "  rpc Put (stream Req) returns (stream Res);", "  // Puts.\n  rpc Put (stream Req) returns (stream Res) { option (scorix.v1.mcp) = {}; option (scorix.v1.rpc) = { effect: MUTATING }; }", 1)
	if _, err := genServer(t, tool); err == nil || !strings.Contains(err.Error(), "Links.Put is bidi and an MCP tool") {
		t.Errorf("a bidi tool: %v", err)
	}
}

func TestGenerateServerAuditsWhatTheTraitSays(t *testing.T) {
	proto := strings.NewReplacer("package acme.links;\n", "package acme.links;\nimport \"scorix/v1/annotations.proto\";\n",
		"rpc Put (Req) returns (Res);", `rpc Put (Req) returns (Res) { option (scorix.v1.rpc) = { audit: false }; }`,
		"rpc Get (Req) returns (Res);", `rpc Get (Req) returns (Res) { option (scorix.v1.rpc) = { audit: true }; }`).Replace(serverProto)
	src, err := genServer(t, proto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`connect.Unary(m, LinksPath+"Put", kernel.Rule{}, h.Put)`, `connect.Unary(m, LinksPath+"Get", kernel.Rule{Audit: true}, h.Get)`} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
}

func TestGenerateServerCarriesReplayIntoTheRule(t *testing.T) {
	proto := strings.NewReplacer("package acme.links;\n", "package acme.links;\nimport \"scorix/v1/annotations.proto\";\n",
		"rpc Put (Req) returns (Res);", `rpc Put (Req) returns (Res) { option (scorix.v1.rpc) = { replay: true permission: "links.write" }; }`).Replace(serverProto)
	src, err := genServer(t, proto)
	if err != nil {
		t.Fatal(err)
	}
	if want := `connect.Unary(m, LinksPath+"Put", kernel.Rule{Permission: "links.write", Replay: true, Audit: true}, h.Put)`; !strings.Contains(src, want) {
		t.Fatalf("generated file lacks %q:\n%s", want, src)
	}
}

func TestGenerateServerAuditsEveryRPCThatIsNotReadOnly(t *testing.T) {
	src, err := genServer(t, strings.Replace(serverProto, "rpc Get (Req)", "rpc GetOne (Req)", 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`connect.Unary(m, LinksPath+"GetOne", kernel.Rule{}, h.GetOne)`,
		`connect.Unary(m, LinksPath+"Put", kernel.Rule{Audit: true}, h.Put)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
}

func TestGenerateServerRegistersEachCommandOnTheCLI(t *testing.T) {
	proto := `syntax = "proto3";
package acme.links;
import "scorix/v1/annotations.proto";
message Req { string id = 1; string token = 2; repeated string tags = 3; }
message Res { string url = 1; }
service LinkAdmin {
  // Drops a link.
  // For good.
  rpc DropLink (Req) returns (Res) {
    option (scorix.v1.cli) = { arg: "id" rest: "tags" secret: "token" timeout: "10s" };
    option (scorix.v1.rpc) = { effect: DESTRUCTIVE };
  }
  // Follows a link.
  rpc Follow (Req) returns (stream Res) { option (scorix.v1.cli) = {}; }
  rpc Get (Req) returns (Res);
}
`
	src, err := genServer(t, proto)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"github.com/tradalab/scorix/transport/cli"`,
		"func RegisterLinkAdminCLI(m *cli.Mux, h LinkAdminHandler) {",
		"cli.Register(m, cli.Command{\n\t\tService:     \"link-admin\",\n\t\tName:        \"drop-link\",\n\t\tDoc:         \"Drops a link. For good.\",\n\t\tProcedure:   LinkAdminPath + \"DropLink\",\n\t\tArg:         \"id\",\n\t\tRest:        \"tags\",\n\t\tSecret:      \"token\",\n\t\tDestructive: true,\n\t\tTimeout:     10000000000,\n\t}, h.DropLink)",
		"cli.RegisterStream(m, cli.Command{\n\t\tService:   \"link-admin\",\n\t\tName:      \"follow\",",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated file lacks %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, `Name:        "get"`) || strings.Contains(src, `Name:      "get"`) {
		t.Errorf("an rpc without (scorix.v1.cli) became a command:\n%s", src)
	}
	plain, err := genServer(t, serverProto)
	if err != nil || strings.Contains(plain, "transport/cli") || strings.Contains(plain, "CLI(") {
		t.Errorf("a proto without commands imports the CLI: %v\n%s", err, plain)
	}
	for name, c := range map[string]struct{ from, to, want string }{
		"bidi":    {"rpc Follow (Req) returns (stream Res)", "rpc Follow (stream Req) returns (stream Res)", "LinkAdmin.Follow is bidi and a command"},
		"exec":    {`arg: "id" rest`, `exec: true arg: "id" rest`, "exec hands a desktop terminal"},
		"no doc":  {"  // Follows a link.\n", "", "with no description"},
		"bad arg": {`arg: "id" rest`, `arg: "tags" rest`, "a positional argument is one word"},
	} {
		if _, err := genServer(t, strings.Replace(proto, c.from, c.to, 1)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

const collideHead = `syntax = "proto3";
package acme.links;
import "scorix/v1/annotations.proto";
message Req { string id = 1; }
message Res { string url = 1; }
message LReq { int32 page_size = 1; string page_token = 2; }
message LRes { repeated Res items = 1; string next_page_token = 2; }
`

func TestGenerateServerRefusesTwoServicesWhoseGoNamesCollide(t *testing.T) {
	list := ` { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 50 order: "id" }; }`
	for name, c := range map[string]struct{ proto, want string }{
		"register http": {collideHead + "service Foo { rpc Get (Req) returns (Res) { option (scorix.v1.http) = { get: \"/a\" }; } }\nservice FooHTTP { rpc Put (Req) returns (Res); }\n", "service Foo and service FooHTTP both generate the Go name RegisterFooHTTP"},
		"path":          {collideHead + "service RegisterFoo { rpc Get (Req) returns (Res); }\nservice FooPath { rpc Put (Req) returns (Res); }\n", "service RegisterFoo and service FooPath both generate the Go name RegisterFooPath"},
		"handler":       {collideHead + "service RegisterFoo { rpc Get (Req) returns (Res); }\nservice FooHandler { rpc Put (Req) returns (Res); }\n", "service RegisterFoo and service FooHandler both generate the Go name RegisterFooHandler"},
		"register mcp":  {collideHead + "service Foo {\n  // Get one.\n  rpc Get (Req) returns (Res) { option (scorix.v1.mcp) = {}; }\n}\nservice FooMCP { rpc Put (Req) returns (Res); }\n", "service Foo and service FooMCP both generate the Go name RegisterFooMCP"},
		"register cli":  {collideHead + "service Foo {\n  // Get one.\n  rpc Get (Req) returns (Res) { option (scorix.v1.cli) = {}; }\n}\nservice FooCLI { rpc Put (Req) returns (Res); }\n", "service Foo and service FooCLI both generate the Go name RegisterFooCLI"},
		"client":        {collideHead + "service Foo { rpc Get (Req) returns (Res); }\nservice foo { rpc Put (Req) returns (Res); }\n", "service Foo and service foo both generate the Go name fooClient"},
		"page spec":     {collideHead + "service Foo { rpc BarList (LReq) returns (LRes)" + list + " }\nservice FooBar { rpc List (LReq) returns (LRes)" + list + " }\n", "rpc Foo.BarList and rpc FooBar.List both generate the Go name FooBarListPage"},
	} {
		if _, err := genServer(t, c.proto); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestGenerateServerAndDispatchNameAMessageTheWayProtocGenGoDoes(t *testing.T) {
	proto := strings.NewReplacer("message Req {", "message user_req {", "(Req)", "(user_req)").Replace(serverProto)
	src, err := genServer(t, proto)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(src, "Get(context.Context, *pb.UserReq) (*pb.Res, error)") || strings.Contains(src, "user_req") {
		t.Errorf("server names the request:\n%s", src)
	}
	d, err := genDispatch(t, proto)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "Get(ctx context.Context, in *pb.UserReq) (*pb.Res, error)") || strings.Contains(d, "user_req") {
		t.Errorf("dispatch names the request:\n%s", d)
	}
}

func TestGenerateServerRefusesAnRPCNamedWithAGoKeyword(t *testing.T) {
	proto := strings.Replace(serverProto, "rpc Get (Req)", "rpc type (Req)", 1)
	if _, err := genServer(t, proto); err == nil || !strings.Contains(err.Error(), "rpc Links.type is named with the Go keyword type") {
		t.Errorf("server err = %v", err)
	}
	if _, err := genDispatch(t, proto); err == nil || !strings.Contains(err.Error(), "rpc Links.type is named with the Go keyword type") {
		t.Errorf("dispatch err = %v", err)
	}
}

func TestGenerateDispatchRefusesTwoServicesWhoseRPCsShareALogic(t *testing.T) {
	proto := collideHead + "service Foo { rpc GetA (Req) returns (Res); }\nservice FooBar { rpc GetA (Req) returns (Res); }\n"
	if _, err := genDispatch(t, proto); err == nil || !strings.Contains(err.Error(), "rpc Foo.GetA and rpc FooBar.GetA both dispatch to logic.NewGetALogic") {
		t.Errorf("err = %v", err)
	}
	if _, err := genServer(t, proto); err != nil {
		t.Errorf("the server output has no shared logic, yet: %v", err)
	}
	proto = collideHead + "service Foo { rpc GetA (Req) returns (Res); }\nservice NewFoo { rpc GetB (Req) returns (Res); }\n"
	if _, err := genDispatch(t, proto); err == nil || !strings.Contains(err.Error(), "service Foo and service NewFoo both generate the Go name NewFooServer") {
		t.Errorf("err = %v", err)
	}
}

func genDispatch(t *testing.T, proto string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "links.proto")
	if err := os.WriteFile(p, []byte(proto), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "server", "dispatch.go")
	err := GenerateDispatch(context.Background(), GenerateDispatchOptions{Proto: p, PB: "example.com/acme/pb", Logic: "example.com/acme/logic", Svc: "example.com/acme/svc", Out: out})
	b, _ := os.ReadFile(out)
	return string(b), err
}
