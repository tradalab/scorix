package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the scorix CLI version",
	RunE: func(cmd *cobra.Command, args []string) error {
		runner.PrintVersion()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	// non-empty Version also enables the `scorix --version` flag.
	rootCmd.Version = runner.Version().Short()
	rootCmd.SetVersionTemplate("scorix {{.Version}}\n")
}
