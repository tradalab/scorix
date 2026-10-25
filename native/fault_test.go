package native

import (
	"errors"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestFault(t *testing.T) {
	if Fault(nil) != nil {
		t.Fatal("Fault(nil) is not nil")
	}
	err := Fault(&Error{Plugin: "photo", Method: "take", Code: "canceled", Message: "closed"})
	if fault.CodeOf(err) != "canceled" || err.Error() != "closed" || !IsCode(err, "canceled") {
		t.Errorf("plugin error became %v (code %q)", err, fault.CodeOf(err))
	}
	if err := Fault(ErrUnavailable); fault.CodeOf(err) != fault.CodeUnavailable || !errors.Is(err, ErrUnavailable) {
		t.Errorf("ErrUnavailable became %v", err)
	}
	mine := fault.Wrap("photo_busy", &Error{Code: "failed", Message: "x"})
	if Fault(mine) != error(mine) {
		t.Error("a fault the caller already coded was re-coded")
	}
	other := errors.New("boom")
	if Fault(other) != other {
		t.Error("an error from elsewhere was rewritten")
	}
}
