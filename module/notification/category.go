package notification

import (
	"strconv"
	"strings"
)

// categoryID names the set of buttons a notification carries: macOS registers
// buttons up front and the notification refers to them by name.
//
// Order is part of the identity, and each part is length-prefixed rather than
// separator-joined, because a label is app text that may contain any byte. Both
// rules exist to stop two different button sets sharing one category, which
// would show the first set's buttons to the second caller; category_test.go has
// the cases.
//
// It lives outside toast_darwin.go so it can be tested on any host.
func categoryID(as []Action) string {
	if len(as) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("scorix.")
	for _, a := range as {
		writeSized(&b, a.Key)
		writeSized(&b, a.Label)
	}
	return b.String()
}

func writeSized(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(len(s)))
	b.WriteByte(':')
	b.WriteString(s)
}
