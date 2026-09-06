package notification

import "testing"

func TestCategoryID(t *testing.T) {
	if got := categoryID(nil); got != "" {
		t.Fatalf("no actions should mean no category, got %q", got)
	}

	yesNo := []Action{{Key: "yes", Label: "Yes"}, {Key: "no", Label: "No"}}
	noYes := []Action{{Key: "no", Label: "No"}, {Key: "yes", Label: "Yes"}}
	if categoryID(yesNo) == categoryID(noYes) {
		t.Fatal("button order is part of the identity: sharing a category renders the wrong order")
	}
	if categoryID(yesNo) != categoryID([]Action{{Key: "yes", Label: "Yes"}, {Key: "no", Label: "No"}}) {
		t.Fatal("the same buttons must reuse one category, or every Notify registers a new one")
	}
	if categoryID(yesNo) == categoryID([]Action{{Key: "yes", Label: "Yep"}, {Key: "no", Label: "No"}}) {
		t.Fatal("a relabelled button is a different category, or the old label sticks")
	}

	// A label is app text and may contain anything, separators included. Two
	// different action sets that encode to the same string would share one
	// category, so the second caller's notification shows the first's buttons.
	two := []Action{{Key: "a", Label: "b"}, {Key: "c", Label: "d"}}
	one := []Action{{Key: "a", Label: "b\x1ec\x1fd"}}
	if categoryID(two) == categoryID(one) {
		t.Fatalf("two action sets collapsed onto one category: %q", categoryID(one))
	}
}
