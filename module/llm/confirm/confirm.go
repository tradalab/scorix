// Package confirm asks the person at the machine with the operating system's own
// dialog. A page cannot draw this one, which is the point: what it guards
// against is a page asking on its own. Separate package so an app that never
// deletes anything does not pull in a dialog library.
package confirm

import (
	"context"
	"errors"

	"github.com/ncruces/zenity"
)

// Native fits llm.Options.Confirm. Blocks until answered; closing the dialog is
// a no.
func Native(_ context.Context, question string) (bool, error) {
	err := zenity.Question(question, zenity.Title("Confirm"), zenity.OKLabel("Delete"), zenity.CancelLabel("Cancel"))
	if errors.Is(err, zenity.ErrCanceled) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
