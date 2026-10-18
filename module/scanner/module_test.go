package scanner

import (
	"context"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestUnavailableWithoutHost(t *testing.T) {
	if _, err := New().ScanQR(context.Background(), struct{}{}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("ScanQR err = %v, want code unavailable", err)
	}
	if _, err := New().ScanImage(context.Background(), struct{}{}); fault.CodeOf(err) != fault.CodeUnavailable {
		t.Fatalf("ScanImage err = %v, want code unavailable", err)
	}
}
