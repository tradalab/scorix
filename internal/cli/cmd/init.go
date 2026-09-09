package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Initialize a new Scorix project",
}

var initDir string

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVarP(&initDir, "dir", "d", ".", "project root directory")
	jsonCommand(initCmd, func(cmd *cobra.Command, out io.Writer) error {
		var name string
		if args := cmd.Flags().Args(); len(args) > 0 {
			name = args[0]
		}
		return runner.Init(cmd.Context(), runner.InitOptions{
			JSONOut: out,
			Name:    name,
			Dir:     initDir,
		})
	})
}
