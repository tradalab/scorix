package share

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestShare(t *testing.T) {
	m := New()
	if _, err := m.Share(context.Background(), Request{}); fault.CodeOf(err) != "invalid_request" {
		t.Fatalf("empty request err = %v, want invalid_request", err)
	}
	if _, err := m.Share(context.Background(), Request{URL: "https://example.com"}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("Share err = %v, want code unavailable", err)
	}
}
