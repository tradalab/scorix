package idl

import (
	"fmt"
	"strings"
	"testing"
)

const head = "syntax = \"proto3\";\npackage app;\nmessage Empty {}\nmessage Req { Empty nothing = 1; }\n"

func parse(t *testing.T, src string) File {
	t.Helper()
	f, err := Parse("app.proto", head+src)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestAMessageNamedEmptyIsFlaggedAndTypedAsAnEmptyRecord(t *testing.T) {
	f := parse(t, "service S { rpc Do (Req) returns (Empty); }")
	if !f.HasEmpty {
		t.Error("HasEmpty is false with a message Empty declared")
	}
	for _, m := range f.Messages {
		if m.Name == "Req" && m.Fields[0].TSType != "Record<string, never>" {
			t.Errorf("an Empty field is typed %q in TS", m.Fields[0].TSType)
		}
	}
}

func TestAnOptionalFieldIsNullableInGoAndTSAndOthersAreNot(t *testing.T) {
	f := parse(t, "message M { optional string s = 1; optional int64 n = 2; optional Empty e = 3; string plain = 4; Empty whole = 5; }\nservice S { rpc Do (M) returns (Empty); }")
	want := map[string]string{
		"s":     "true *string string | null",
		"n":     "true *int64 number | null",
		"e":     "true *Empty Record<string, never> | null",
		"plain": "false string string",
		"whole": "false Empty Record<string, never>",
	}
	for _, m := range f.Messages {
		if m.Name != "M" {
			continue
		}
		for _, fl := range m.Fields {
			if got := fmt.Sprintf("%v %s %s", fl.Optional, fl.GoType, fl.TSType); got != want[fl.Name] {
				t.Errorf("field %s = %q, want %q", fl.Name, got, want[fl.Name])
			}
		}
	}
}

func TestAnRPCMiddlewareJoinsTheFileList(t *testing.T) {
	f := parse(t, "service S {\n  // @middleware Audit\n  rpc Do (Req) returns (Empty);\n}")
	if len(f.Middlewares) != 1 || f.Middlewares[0] != "Audit" || !f.HasMiddleware {
		t.Fatalf("Middlewares = %v, HasMiddleware = %v, want the rpc's Audit", f.Middlewares, f.HasMiddleware)
	}
}

func TestEachStreamShapeHasItsArity(t *testing.T) {
	f := parse(t, `service S {
  rpc A (Req) returns (Empty);
  rpc B (Req) returns (stream Empty);
  rpc C (stream Req) returns (Empty);
  rpc D (stream Req) returns (stream Empty);
}`)
	want := []string{"unary", "server-stream", "client-stream", "bidi"}
	for i, rpc := range f.Services[0].RPCs {
		if rpc.Arity != want[i] {
			t.Errorf("%s arity = %q, want %q", rpc.Name, rpc.Arity, want[i])
		}
	}
}

func TestAServiceWithoutRPCsIsRefused(t *testing.T) {
	_, err := Parse("app.proto", head+"service S {}")
	if err == nil || !strings.Contains(err.Error(), "service S has no rpc methods") {
		t.Fatalf("Parse = %v, want the empty service refused", err)
	}
}

func TestAFileWithoutASyntaxLineIsNotReadAsProto3(t *testing.T) {
	if _, err := Parse("app.proto", "message M { string a = 1; }\nservice S { rpc Do (M) returns (M); }"); err == nil {
		t.Fatal("a file with no syntax statement compiled, so its dialect was guessed")
	}
}

func TestAnEnumAndAMapFieldCarryWhatTheGeneratorsNeed(t *testing.T) {
	f := parse(t, `enum Priority {
  PRIORITY_UNSPECIFIED = 0;
  HIGH = 1;
}
message Inner { string id = 1; }
message M {
  Priority p = 1;
  map<string, int64> counts = 2;
  map<string, Inner> by_id = 3;
  repeated Priority ps = 4;
  map<string, Priority> levels = 5;
}
service S { rpc Do (M) returns (Empty); }`)
	got := map[string]string{}
	for _, m := range f.Messages {
		if m.Name != "M" {
			continue
		}
		for _, fl := range m.Fields {
			got[fl.Name] = fmt.Sprintf("%s rep=%v map=%v key=%s enum=%v", fl.Type, fl.Repeated, fl.Map, fl.MapKey, fl.Enum)
		}
	}
	want := map[string]string{
		"p":      "Priority rep=false map=false key= enum=[PRIORITY_UNSPECIFIED HIGH]",
		"counts": "int64 rep=false map=true key=string enum=[]",
		"by_id":  "Inner rep=false map=true key=string enum=[]",
		"ps":     "Priority rep=true map=false key= enum=[PRIORITY_UNSPECIFIED HIGH]",
		"levels": "Priority rep=false map=true key=string enum=[PRIORITY_UNSPECIFIED HIGH]",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("field %s = %q, want %q", name, got[name], w)
		}
	}
	for _, m := range f.Messages {
		if strings.HasSuffix(m.Name, "Entry") {
			t.Errorf("a map entry %s leaked into the messages", m.Name)
		}
	}
}

func TestAMapFieldIsTypedAsAMapInGoAndARecordWithStringKeysInTS(t *testing.T) {
	f := parse(t, `enum Priority { PRIORITY_UNSPECIFIED = 0; }
message Inner { string id = 1; }
message M {
  map<string, int64> counts = 1;
  map<int32, Inner> by_num = 2;
  map<bool, Priority> flags = 3;
  map<uint64, bytes> blobs = 4;
}
service S { rpc Do (M) returns (Empty); }`)
	want := map[string][2]string{
		"counts": {"map[string]int64", "Record<string, number>"},
		"by_num": {"map[int32]Inner", "Record<string, Inner>"},
		"flags":  {"map[bool]Priority", "Record<string, Priority>"},
		"blobs":  {"map[uint64][]byte", "Record<string, Uint8Array>"},
	}
	for _, m := range f.Messages {
		if m.Name != "M" {
			continue
		}
		for _, fl := range m.Fields {
			if got := [2]string{fl.GoType, fl.TSType}; got != want[fl.Name] {
				t.Errorf("field %s types = %q, want %q", fl.Name, got, want[fl.Name])
			}
		}
	}
}

func TestAMapBesideThePageDoesNotCountAsASecondRepeatedMessage(t *testing.T) {
	f := parse(t, `import "scorix/v1/annotations.proto";
message Item { string id = 1; }
message ListReq { int32 page_size = 1; string page_token = 2; }
message ListResp { repeated Item items = 1; string next_page_token = 2; map<string, Item> by_id = 3; }
service S { rpc ListItems (ListReq) returns (ListResp) { option (scorix.v1.list) = { default_page_size: 10 max_page_size: 50 order: "id" }; } }`)
	if f.Services[0].RPCs[0].List == nil {
		t.Fatal("the list trait was not read")
	}
}

func TestAnAppProtoStillRefusesEnumAndMap(t *testing.T) {
	for word, src := range map[string]string{
		"enum": "syntax = \"proto3\";\nenum Color { RED = 0; }\nmessage M { string a = 1; }\nservice S { rpc Do(M) returns (M); }",
		"map":  "syntax = \"proto3\";\nmessage M { map<string, string> tags = 1; }\nservice S { rpc Do(M) returns (M); }",
	} {
		if _, err := Parse("app.proto", src); err != nil {
			t.Errorf("Parse refused %s: %v", word, err)
		}
		if _, err := ParseApp("app.proto", src); err == nil || !strings.Contains(err.Error(), `"`+word+`" is not supported`) {
			t.Errorf("ParseApp accepted %s: %v", word, err)
		}
	}
}

func TestANestedEnumInAServiceProtoIsToldToMoveToTheTopLevel(t *testing.T) {
	_, err := Parse("app.proto", "syntax = \"proto3\";\npackage app;\nmessage M { enum K { A = 0; } K k = 1; }\nservice S { rpc Do (M) returns (M); }\n")
	if err == nil || !strings.Contains(err.Error(), "enum K is declared inside M; declare it at the top level") {
		t.Fatalf("Parse = %v", err)
	}
}
