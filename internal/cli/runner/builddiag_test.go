package runner

import "testing"

func TestParseGoDiagnostic(t *testing.T) {
	cases := []struct {
		in   string
		file string
		line int
		col  int
		msg  string
	}{
		{`.\main.go:97:29: cannot use "nope" as int value`, "main.go", 97, 29, `cannot use "nope" as int value`},
		{"internal/svc/x.go:12:3: undefined: Foo", "internal/svc/x.go", 12, 3, "undefined: Foo"},
		{"main.go:2: syntax error", "main.go", 2, 0, "syntax error"},
		// The drive letter is why this is a regexp and not a strings.Split on ":".
		{`C:\src\app\main.go:5:1: expected 'package'`, "C:/src/app/main.go", 5, 1, "expected 'package'"},
		{"link: running gcc failed", "", 0, 0, "link: running gcc failed"},
	}
	for _, c := range cases {
		got := parseGoDiagnostic("example.com/app", c.in)
		if got.File != c.file || got.Line != c.line || got.Col != c.col || got.Message != c.msg {
			t.Errorf("%q\n got %+v\nwant file=%q line=%d col=%d msg=%q", c.in, got, c.file, c.line, c.col, c.msg)
		}
	}
}
