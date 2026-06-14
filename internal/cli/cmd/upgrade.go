package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var upgradeCmd = &cobra.Command{
	Use:   "upgrade [version]",
	Short: "Upgrade the scorix CLI via go install (default: latest)",
	Long: "Reinstall the scorix CLI with `go install`.\n" +
		"No arg installs `latest` (highest release tag); pass `main` for the\n" +
		"newest commit, or a specific tag / pseudo-version.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var ref string
		if len(args) == 1 {
			ref = args[0]
		}
		return runner.Upgrade(cmd.Context(), ref)
	},
}

func init() {
	rootCmd.AddCommand(upgradeCmd)
}
