package llamacpp

import (
	"fmt"
	"testing"

	"github.com/tradalab/scorix/proc"
)

// The port is picked by opening a listener and closing it again, so anything
// on the machine can take it before the child binds. Losing that race costs
// the whole ready timeout - three minutes by default - and reads as a broken
// engine, which is what a reinstall does not fix.
func TestAPortLostToSomethingElseIsPickedAgain(t *testing.T) {
	for _, line := range []string{
		"error: couldn't bind HTTP server socket, hostname: 127.0.0.1, port: 51234",
		"bind: address already in use",
		"Only one usage of each socket address (protocol/network address/port) is normally permitted.",
	} {
		if !proc.PortTaken(fmt.Errorf("proc: llama-server exited before becoming healthy (exit 1)\n%s", line)) {
			t.Errorf("not read as a lost port: %q", line)
		}
	}
	for _, line := range []string{
		"error: unable to load model",
		"failed to fit params to free device memory",
		"exit status 3221225477",
	} {
		if proc.PortTaken(fmt.Errorf("proc: llama-server exited before becoming healthy (exit 1)\n%s", line)) {
			t.Errorf("read as a lost port: %q", line)
		}
	}
}
