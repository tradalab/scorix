package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/cli/runner"
)

var createCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Generate a new Scorix project",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Create(cmd.Context(), args)
	},
}

func init() {
	rootCmd.AddCommand(createCmd)
}
