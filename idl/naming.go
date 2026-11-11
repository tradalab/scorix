package idl

import (
	"strings"
	"unicode"
)

// ExportedName turns proto_name, protoName or proto-name into ProtoName.
func ExportedName(name string) string {
	parts := splitIdentifier(name)
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

// ProtoGoName is the Go type name protoc-gen-go gives the top-level proto message name.
func ProtoGoName(name string) string {
	var b []byte
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == '_' && i == 0:
			b = append(b, 'X')
		case c == '_' && i+1 < len(name) && isLower(name[i+1]):
		case isDigit(c):
			b = append(b, c)
		default:
			if isLower(c) {
				c -= 'a' - 'A'
			}
			b = append(b, c)
			for ; i+1 < len(name) && isLower(name[i+1]); i++ {
				b = append(b, name[i+1])
			}
		}
	}
	return string(b)
}

func isLower(c byte) bool { return 'a' <= c && c <= 'z' }

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// LowerCamel is ExportedName with the first letter lowered.
func LowerCamel(name string) string {
	out := ExportedName(name)
	if out == "" {
		return ""
	}
	return strings.ToLower(out[:1]) + out[1:]
}

// KebabName joins the words of an identifier with hyphens, lowercased.
func KebabName(name string) string {
	return strings.Join(splitIdentifier(name), "-")
}

// SnakeName joins the words of an identifier with underscores, lowercased.
func SnakeName(name string) string {
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
