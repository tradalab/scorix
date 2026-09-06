package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	cfgFile string
)

// Execute maps a failure onto the documented exit statuses. The message goes to
// STDERR: in --json mode stdout already carries the result document, and a
// second line there would break every parser reading it.
func Execute() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	var ee *runner.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.Code)
	}
	os.Exit(runner.ExitFailed)
}

var rootCmd = &cobra.Command{
	Use:   "scorix",
	Short: "Scorix CLI – build native apps with Go + WebUI",
	Long:  "Scorix CLI.\nBuild, scaffold and manage Scorix applications.",
	// Runtime failures (drift, parse error) aren't usage mistakes; Execute already prints the error.
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cmd.SetContext(context.Background())
		return nil
	},
}

func init() {
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return runner.UsageError(err)
	})
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config path (optional)")
}
