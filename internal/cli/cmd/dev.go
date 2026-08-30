package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var devCmd = &cobra.Command{
	Use:   "dev",
	Short: "Start the project in development mode (shell dev server + HMR)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, _ := cmd.Flags().GetString("dir")
		url, _ := cmd.Flags().GetString("url")
		legacy, _ := cmd.Flags().GetBool("legacy")
		watch, _ := cmd.Flags().GetBool("watch")
		return runner.Dev(cmd.Context(), runner.DevOptions{
			Dir:      dir,
			URL:      url,
			Legacy:   legacy,
			Watch:    watch,
			WatchSet: cmd.Flags().Changed("watch"),
		})
	},
}

func init() {
	rootCmd.AddCommand(devCmd)
	devCmd.Flags().StringP("dir", "d", ".", "project root directory")
	devCmd.Flags().String("url", "", "use an already-running frontend dev server (skip spawning pnpm dev)")
	devCmd.Flags().Bool("legacy", false, "build the shell once instead of running the dev server (no HMR)")
	devCmd.Flags().Bool("watch", true, "rebuild + relaunch the Go app on source changes; proto/schema changes regenerate first (scorix.yaml dev.hot_reload)")
}
