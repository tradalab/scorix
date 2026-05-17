package runner

import (
	"strings"
	"unicode"
)

func exportedName(name string) string {
	parts := splitIdentifier(name)
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func lowerCamel(name string) string {
	out := exportedName(name)
	if out == "" {
		return ""
	}
	return strings.ToLower(out[:1]) + out[1:]
}

func kebabName(name string) string {
	return strings.Join(splitIdentifier(name), "-")
}

func snakeName(name string) string {
	return strings.Join(splitIdentifier(name), "_")
}

func splitIdentifier(name string) []string {
	var words []string
	var buf []rune
	runes := []rune(strings.TrimSpace(name))
	flush := func() {
		if len(buf) == 0 {
			return
		}
		words = append(words, strings.ToLower(string(buf)))
		buf = buf[:0]
	}
	for i, r := range runes {
		if r == '_' || r == '-' || r == '.' || unicode.IsSpace(r) {
			flush()
			continue
		}
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
			if len(buf) > 0 && (unicode.IsLower(prev) || unicode.IsDigit(prev) || nextLower) {
				flush()
			}
		}
		buf = append(buf, unicode.ToLower(r))
	}
	flush()
	if len(words) == 0 {
		return []string{"value"}
	}
	return words
}
