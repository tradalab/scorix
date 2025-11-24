package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/cmd/scorix/runner"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Start dev server",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Dev(cmd.Context())
	},
}

func init() {
	rootCmd.AddCommand(devCmd)
}
