package fault

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeOfThroughChain(t *testing.T) {
	base := Errorf("quota_exceeded", "over the limit").With("limit", 5)
	wrapped := fmt.Errorf("saving profile: %w", base)
	if got := CodeOf(wrapped); got != "quota_exceeded" {
		t.Fatalf("CodeOf(wrapped) = %q", got)
	}
	if got := CodeOf(errors.New("plain")); got != "" {
		t.Fatalf("CodeOf(plain) = %q, want empty", got)
	}
	if base.Error() != "over the limit" {
		t.Fatalf("Error() = %q — must be the message only, code travels separately", base.Error())
	}
}

func TestWrapKeepsCause(t *testing.T) {
	cause := errors.New("disk full")
	e := Wrap(CodeUnavailable, cause)
	if !errors.Is(e, cause) {
		t.Fatal("Wrap must keep the cause visible to errors.Is")
	}
	if e.Code != CodeUnavailable || e.Message != "disk full" {
		t.Fatalf("Wrap = {%q %q}", e.Code, e.Message)
	}
	if Wrap("x", nil) != nil {
		t.Fatal("Wrap(nil) must be nil")
	}
}
