// Package desktopentry edits freedesktop .desktop files.
package desktopentry

import (
	"maps"
	"slices"
	"strings"
)

const mainGroup = "[Desktop Entry]"

func Get(src []byte, key string) string {
	group := ""
	for line := range strings.SplitSeq(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			group = t
			continue
		}
		if k, v, ok := strings.Cut(t, "="); ok && group == mainGroup && strings.TrimSpace(k) == key {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Rewrite sets keys in the main group and, when cmd != "", the command of every Exec.
func Rewrite(src []byte, cmd string, keys map[string]string) []byte {
	lines := strings.Split(strings.TrimRight(string(src), "\n"), "\n")
	var out []string
	done := map[string]bool{}
	group := ""
	flush := func() {
		if group != mainGroup {
			return
		}
		end := len(out)
		for end > 0 && strings.TrimSpace(out[end-1]) == "" {
			end--
		}
		var add []string
		for _, k := range slices.Sorted(maps.Keys(keys)) {
			if !done[k] {
				add = append(add, k+"="+keys[k])
				done[k] = true
			}
		}
		out = slices.Insert(out, end, add...)
	}
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "[") {
			flush()
			group = t
			out = append(out, line)
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		// The spec allows spaces around the =.
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if ok && group == mainGroup {
			if nv, set := keys[k]; set {
				if done[k] {
					continue
				}
				v, line, done[k] = nv, k+"="+nv, true
			}
		}
		if ok && k == "Exec" && cmd != "" {
			line = "Exec=" + cmd + args(v)
		}
		out = append(out, line)
	}
	flush()
	return []byte(strings.Join(out, "\n") + "\n")
}

func Quote(s string) string {
	// A bare % would read as a field code.
	s = strings.ReplaceAll(s, "%", "%%")
	if !strings.ContainsAny(s, " \t\n\"'\\><~|&;$*?#()`") {
		return s
	}
	r := strings.NewReplacer(`"`, `\"`, "`", "\\`", `$`, `\$`, `\`, `\\`).Replace(s)
	return `"` + strings.ReplaceAll(r, `\`, `\\`) + `"`
}

func args(exec string) string {
	if strings.HasPrefix(exec, `"`) {
		for i := 1; i < len(exec); i++ {
			switch exec[i] {
			case '\\':
				i++
			case '"':
				return exec[i+1:]
			}
		}
		return ""
	}
	if i := strings.IndexByte(exec, ' '); i >= 0 {
		return exec[i:]
	}
	return ""
}
