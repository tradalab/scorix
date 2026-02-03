package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/cmd/scorix/runner"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check environment & dependencies",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Doctor(cmd.Context())
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}
