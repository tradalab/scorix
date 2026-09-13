package devctl

import "testing"

func TestOnlyTheYesWordsOpenTheSocket(t *testing.T) {
	for _, v := range []string{"1", "true", "on", "yes", "TRUE", " yes "} {
		if !On(v) {
			t.Errorf("On(%q) = false: the socket stays shut when something asked for it", v)
		}
	}
	// The half that matters: this gate guards code execution in the user's
	// session, so anything that is not a yes - a leftover "0", a typo, the "y"
	// someone types expecting it to count - has to leave it shut.
	for _, v := range []string{"", "0", "false", "off", "no", "please", "y"} {
		if On(v) {
			t.Errorf("On(%q) = true: a value that is not a yes opened the socket", v)
		}
	}
}
