package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/cli/runner"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Start the project in development mode",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		return runner.Dev(cmd.Context(), runner.DevOptions{
			Dir: dir,
		})
	},
}

func init() {
	rootCmd.AddCommand(devCmd)
	devCmd.Flags().StringP("dir", "d", ".", "project root directory")
}
