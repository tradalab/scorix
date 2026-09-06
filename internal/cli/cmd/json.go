package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"
)

// One call so the next command has nothing to remember: a missing restore leaks
// the swap into the next run, a missing swap corrupts the document.
func jsonCommand(c *cobra.Command, run func(cmd *cobra.Command, out io.Writer) error) {
	var enabled bool
	c.Flags().BoolVar(&enabled, "json", false, "emit one JSON result on stdout; human output moves to stderr")
	c.RunE = func(cmd *cobra.Command, args []string) error {
		out, restore := jsonOut(enabled)
		defer restore()
		return run(cmd, out)
	}
}

// jsonOut redirects every print in the process for the length of the call, which
// works because fmt.Print* and the subprocess wiring both read os.Stdout when
// they run. Threading a writer through the 90-odd print sites instead would fail
// OPEN: one forgotten call site corrupts the document, and only on that path.
func jsonOut(enabled bool) (w io.Writer, restore func()) {
	if !enabled {
		return nil, func() {}
	}
	real := os.Stdout
	os.Stdout = os.Stderr
	return real, func() { os.Stdout = real }
}
