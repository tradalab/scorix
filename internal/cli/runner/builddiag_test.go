package runner

import (
	"runtime"
	"testing"
)

func TestParseGoDiagnostic(t *testing.T) {
	type diagCase struct {
		in   string
		file string
		line int
		col  int
		msg  string
	}
	cases := []diagCase{
		{"internal/svc/x.go:12:3: undefined: Foo", "internal/svc/x.go", 12, 3, "undefined: Foo"},
		{"main.go:2: syntax error", "main.go", 2, 0, "syntax error"},
		{`./main.go:97:29: cannot use "nope" as int value`, "main.go", 97, 29, `cannot use "nope" as int value`},
		{"link: running gcc failed", "", 0, 0, "link: running gcc failed"},
	}
	// Only a Windows toolchain emits backslashes, and filepath.ToSlash only rewrites
	// them there - on unix a backslash is a legal filename character, so normalising
	// everywhere would corrupt real paths for an input that cannot arrive.
	if runtime.GOOS == "windows" {
		cases = append(cases,
			diagCase{`.\main.go:97:29: cannot use "nope" as int value`, "main.go", 97, 29, `cannot use "nope" as int value`},
			// The drive letter is why this is a regexp and not a strings.Split on ":".
			diagCase{`C:\src\app\main.go:5:1: expected 'package'`, "C:/src/app/main.go", 5, 1, "expected 'package'"},
		)
	}
	for _, c := range cases {
		got := parseGoDiagnostic("example.com/app", c.in)
		if got.File != c.file || got.Line != c.line || got.Col != c.col || got.Message != c.msg {
			t.Errorf("%q\n got %+v\nwant file=%q line=%d col=%d msg=%q", c.in, got, c.file, c.line, c.col, c.msg)
		}
	}
}
