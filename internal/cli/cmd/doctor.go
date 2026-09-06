package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check environment & dependencies",
}

func init() {
	rootCmd.AddCommand(doctorCmd)
	jsonCommand(doctorCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Doctor(cmd.Context(), runner.DoctorOptions{JSONOut: out})
	})
}
