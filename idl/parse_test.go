package idl

import (
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
