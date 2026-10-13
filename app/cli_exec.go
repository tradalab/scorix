package app

import (
	"runtime"
	"strings"
)

// ExecSpec is the reply of an `@cli exec` rpc. Apps declare the message in their
// own proto (the parser has no imports); the generator checks its shape.
type ExecSpec struct {
	Path  string   `json:"path"`
	Args  []string `json:"args"`
	Env   []EnvVar `json:"env"`
	Unset []string `json:"unset"`
	Dir   string   `json:"dir"`
}

type EnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Set names are dropped first: with two entries for one name, which one wins is
// up to the platform.
func execEnv(base []string, spec ExecSpec) []string {
	drop := map[string]bool{}
	for _, n := range spec.Unset {
		drop[envKey(n)] = true
	}
	for _, e := range spec.Env {
		drop[envKey(e.Name)] = true
	}
	out := make([]string, 0, len(base)+len(spec.Env))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if !drop[envKey(name)] {
			out = append(out, kv)
		}
	}
	for _, e := range spec.Env {
		out = append(out, e.Name+"="+e.Value)
	}
	return out
}

// cmd.exe parses the line again and acts on & | < > ^ % before the script sees
// it, so `run -- "fix A & B"` would run B. Same escaping as cross-spawn.
func cmdLine(script string, args []string) string {
	parts := []string{caretEscape(script)}
	for _, a := range args {
		parts = append(parts, caretEscape(argvQuote(a)))
	}
	return `/d /s /c "` + strings.Join(parts, " ") + `"`
}

func argvQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, r := range s {
		switch r {
		case '\\':
			slashes++
			continue
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes*2+1))
		default:
			b.WriteString(strings.Repeat(`\`, slashes))
		}
		slashes = 0
		b.WriteRune(r)
	}
	b.WriteString(strings.Repeat(`\`, slashes*2))
	b.WriteByte('"')
	return b.String()
}

func caretEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`()[]%!^"<>&|;, *?`+"`", r) {
			b.WriteByte('^')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}
