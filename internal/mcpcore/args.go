package mcpcore

import "fmt"

// Args is one tool call's arguments, already unmarshalled.
type Args map[string]any

func (a Args) Str(key, def string) string {
	if s, ok := a[key].(string); ok && s != "" {
		return s
	}
	return def
}

func (a Args) Truthy(key string) bool {
	b, _ := a[key].(bool)
	return b
}

// TruthyOr keeps a flag whose CLI default is true (dev --watch) at true when the
// caller says nothing, instead of flipping it to Go's zero value.
func (a Args) TruthyOr(key string, def bool) bool {
	if b, ok := a[key].(bool); ok {
		return b
	}
	return def
}

func (a Args) Num(key string, def int) int {
	if f, ok := a[key].(float64); ok && f > 0 {
		return int(f)
	}
	return def
}

func (a Args) Strs(key string) []string {
	raw, ok := a[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func Obj(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

func StrProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func BoolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}

func IntProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func ListProp(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": desc,
	}
}

// CheckArgs rejects an argument whose JSON type contradicts the schema the tool
// published. Ignoring it instead is worse than an error: a dir that is not a
// string falls back to "." and the command runs against the wrong directory.
func CheckArgs(schema map[string]any, given map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	for key, val := range given {
		spec, ok := props[key].(map[string]any)
		if !ok || val == nil {
			continue // unknown keys and explicit nulls are the caller's business
		}
		want, _ := spec["type"].(string)
		if !matchesJSONType(want, val) {
			return fmt.Errorf("argument %q must be %s, got %T", key, want, val)
		}
	}
	return nil
}

func matchesJSONType(want string, val any) bool {
	switch want {
	case "string":
		_, ok := val.(string)
		return ok
	case "boolean":
		_, ok := val.(bool)
		return ok
	case "integer", "number":
		_, ok := val.(float64)
		return ok
	case "array":
		_, ok := val.([]any)
		return ok
	case "object":
		_, ok := val.(map[string]any)
		return ok
	}
	return true // no declared type: nothing to contradict
}
