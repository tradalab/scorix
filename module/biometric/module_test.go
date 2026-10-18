package biometric

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

// The frontend branches on the code, so a transport error here would break it.
func TestUnavailableWithoutHost(t *testing.T) {
	m := New()
	if _, err := m.Status(context.Background(), struct{}{}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("Status err = %v, want code unavailable", err)
	}
	if _, err := m.Authenticate(context.Background(), Prompt{Title: "x"}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("Authenticate err = %v, want code unavailable", err)
	}
}
