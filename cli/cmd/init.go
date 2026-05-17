package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/cli/runner"
)

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Initialize a new Scorix project",
	RunE: func(cmd *cobra.Command, args []string) error {
		var name string
		if len(args) > 0 {
			name = args[0]
		}
		return runner.Init(cmd.Context(), runner.InitOptions{
			Name: name,
			Dir:  initDir,
		})
	},
}

var initDir string

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVarP(&initDir, "dir", "d", ".", "project root directory")
}
