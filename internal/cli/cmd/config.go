package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	configManifest string
	configOverlay  string
	configEnv      bool
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Inspect the resolved app config and its env override surface",
	Long: "Inspect configuration.\n\n" +
		"  scorix config --resolved            effective config + per-key source (secrets masked)\n" +
		"  scorix config --resolved --overlay runtime.yaml   simulate a runtime overlay file\n" +
		"  scorix config --env                 list the env vars the config accepts (from `env` tags)\n",
	RunE: func(cmd *cobra.Command, args []string) error {
		if configEnv {
			return runner.ConfigEnv(configManifest)
		}
		return runner.ConfigResolved(configManifest, configOverlay)
	},
}

func init() {
	configCmd.Flags().StringVar(&configManifest, "manifest", "", "embedded manifest path (default: scorix.yaml, then etc/app.yaml)")
	configCmd.Flags().StringVar(&configOverlay, "overlay", "", "runtime overlay YAML to simulate (env > overlay > embedded)")
	configCmd.Flags().BoolVar(&configEnv, "env", false, "list the env-var surface instead of resolved values")
	configCmd.Flags().Bool("resolved", true, "show resolved config (default)")
	rootCmd.AddCommand(configCmd)
}
