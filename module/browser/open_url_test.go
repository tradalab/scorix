package browser

import (
	"encoding/json"
	"testing"
)

func TestOpenUrlRequestTakesBothShapes(t *testing.T) {
	for _, raw := range []string{`"https://example.com"`, `{"url":"https://example.com"}`} {
		var r OpenUrlRequest
		if err := json.Unmarshal([]byte(raw), &r); err != nil || r.URL != "https://example.com" {
			t.Errorf("%s: got %q, %v", raw, r.URL, err)
		}
	}
	var r OpenUrlRequest
	if err := json.Unmarshal([]byte(`42`), &r); err == nil {
		t.Error("a number was taken as a request")
	}
}
