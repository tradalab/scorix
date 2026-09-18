package mcpcore

import (
	"strings"
	"testing"
)

func TestCheckArgsRefusesWhatAClosedSchemaDoesNotName(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		"connection_id": map[string]any{"type": "string"},
		"filter": map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"pattern": map[string]any{"type": "string"},
		}},
		"limit": map[string]any{"type": "integer"},
		"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}}
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"misspelt key":           {map[string]any{"conection_id": "x"}, `"conection_id"`},
		"misspelt nested key":    {map[string]any{"filter": map[string]any{"patern": "a*"}}, `"filter.patern"`},
		"fraction for integer":   {map[string]any{"limit": 1.5}, "integer"},
		"wrong type in an array": {map[string]any{"tags": []any{"a", 3.0}}, "tags"},
	} {
		if err := CheckArgs(schema, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to name %s", name, err, c.want)
		}
	}
	if err := CheckArgs(schema, map[string]any{"conection_id": "x"}); err == nil || !strings.Contains(err.Error(), "connection_id") {
		t.Errorf("the refusal does not tell the model what the tool does take: %v", err)
	}
	valid := map[string]any{"connection_id": "x", "filter": map[string]any{"pattern": "a*"}, "limit": 10.0, "tags": []any{"a"}}
	if err := CheckArgs(schema, valid); err != nil {
		t.Errorf("valid arguments were refused: %v", err)
	}
}

// The CLI tools publish open schemas; tightening the check must not change them.
func TestCheckArgsLeavesAnOpenSchemaOpen(t *testing.T) {
	open := map[string]any{"type": "object", "properties": map[string]any{"dir": map[string]any{"type": "string"}}}
	if err := CheckArgs(open, map[string]any{"dir": ".", "extra": true}); err != nil {
		t.Errorf("an unknown key was refused by a schema that never closed itself: %v", err)
	}
}
