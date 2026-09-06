//go:build !server && darwin

package systemtray

import (
	"testing"

	"github.com/ebitengine/purego/objc"
	"github.com/tradalab/scorix/internal/mac"
)

// Builds a real NSMenu on the CI macos runner. It touches no status bar, so it
// needs no window server: this is the menu shape, not the menu on screen.
func TestBuildMenuShape(t *testing.T) {
	if err := mac.Init(); err != nil {
		t.Fatalf("mac.Init: %v", err)
	}
	noop := func() {}
	nodes := []node{
		{label: "Open", onClick: noop},
		{separator: true},
		{label: "Parent", children: []node{{label: "Inner", onClick: noop}}},
		{label: "Off", disabled: true, onClick: noop},
		{label: "Ticked", checked: true, onClick: noop},
		{label: "Just text"}, // no action: display-only
	}
	m := buildMenu(nodes)
	if m == 0 {
		t.Fatal("buildMenu returned nil")
	}
	if n := objc.Send[int64](m, mac.Sel("numberOfItems")); n != int64(len(nodes)) {
		t.Fatalf("numberOfItems = %d, want %d", n, len(nodes))
	}

	at := func(i int) objc.ID {
		return objc.Send[objc.ID](m, mac.Sel("itemAtIndex:"), int64(i))
	}
	// An Objective-C BOOL is a signed char and leaves the rest of the return
	// register undefined, so it is read as a word and masked rather than trusted
	// to a bool conversion.
	isYes := func(id objc.ID, s objc.SEL) bool { return objc.Send[uint64](id, s)&1 == 1 }
	if !isYes(at(1), mac.Sel("isSeparatorItem")) {
		t.Error("index 1 should be a separator")
	}
	if objc.Send[objc.ID](at(2), mac.Sel("submenu")) == 0 {
		t.Error("index 2 should carry a submenu")
	}
	// The three that regressed before: Disabled was dropped for submenu rows,
	// checked state was never read back, and a display-only row must not look
	// clickable.
	if isYes(at(3), mac.Sel("isEnabled")) {
		t.Error("a disabled item came back enabled")
	}
	if got := objc.Send[int64](at(4), mac.Sel("state")); got != controlStateOn {
		t.Errorf("checked item state = %d, want %d", got, controlStateOn)
	}
	if isYes(at(5), mac.Sel("isEnabled")) {
		t.Error("an item with no action must not be enabled")
	}
	if got := mac.GoString(objc.Send[objc.ID](at(0), mac.Sel("title"))); got != "Open" {
		t.Errorf("title = %q, want \"Open\"", got)
	}
	// A click has to be able to find its way back to the Go closure.
	if objc.Send[objc.ID](at(0), mac.Sel("target")) == 0 {
		t.Error("an actionable item has no target: its click would go nowhere")
	}
	if objc.Send[int64](at(0), mac.Sel("tag")) == 0 {
		t.Error("an actionable item has no tag: the handler cannot be looked up")
	}
}
