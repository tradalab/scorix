package store

import (
	"encoding/json"
	"testing"

	"github.com/tradalab/scorix/module/moduletest"
)

func TestAllReturnsEveryKeyAndValueReadsOneFromGo(t *testing.T) {
	m := New()
	r := moduletest.Load(t, m, nil)
	for k, v := range map[string]any{"language": "vi", "autoupdate": "true"} {
		if _, err := r.Call("Set", map[string]any{"key": k, "value": v}); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := r.Call("All", nil)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]string
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all["language"] != "vi" || all["autoupdate"] != "true" {
		t.Fatalf("All = %v", all)
	}
	if v, ok := m.Value("language"); !ok || v != "vi" {
		t.Fatalf("Value(language) = %v, %v", v, ok)
	}
	if _, ok := m.Value("missing"); ok {
		t.Fatal("Value reported a key that was never set")
	}
	if got := r.Needs("All"); got != "store" {
		t.Fatalf("All needs %q, want store", got)
	}
}

func TestValueAndAllHandOutCopiesTheCallerMayChange(t *testing.T) {
	m := New()
	r := moduletest.Load(t, m, nil)
	if _, err := r.Call("Set", map[string]any{"key": "prefs", "value": map[string]any{"tabs": []any{"a"}}}); err != nil {
		t.Fatal(err)
	}
	v, _ := m.Value("prefs")
	v.(map[string]any)["tabs"].([]any)[0] = "changed"
	all, _ := m.All(nil)
	all["prefs"].(map[string]any)["extra"] = true
	again, _ := m.Value("prefs")
	if got := again.(map[string]any); got["tabs"].([]any)[0] != "a" || got["extra"] != nil {
		t.Fatalf("the store changed through a returned value: %v", got)
	}
}
