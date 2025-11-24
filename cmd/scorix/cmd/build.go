package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/cmd/scorix/runner"
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Build Scorix application",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Build(cmd.Context(), runner.BuildOptions{
			Target:  target,
			Release: isRelease,
		})
	},
}

var (
	target    string
	isRelease bool
)

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringVar(&target, "target", "", "Target OS (windows, linux, macos)")
	buildCmd.Flags().BoolVar(&isRelease, "release", false, "Build release mode")
}
