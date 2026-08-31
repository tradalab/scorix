package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	appcastDir       string
	appcastArtifacts string
	appcastBaseURLs  []string
)

var appcastCmd = &cobra.Command{
	Use:   "appcast",
	Short: "Sign release artifacts (Ed25519) and write SHA256SUMS + appcast.json",
	Long: "Sign the installer artifacts and emit the update channel manifest.\n\n" +
		"Run after `scorix package` has collected installers (typically once, over a\n" +
		"directory holding all per-OS artifacts). The Ed25519 private key is read from\n" +
		"the env named in package.update.sign_key_env.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Appcast(cmd.Context(), runner.AppcastOptions{
			Dir:          appcastDir,
			ArtifactsDir: appcastArtifacts,
			BaseURLs:     appcastBaseURLs,
		})
	},
}

func init() {
	rootCmd.AddCommand(appcastCmd)
	appcastCmd.Flags().StringVarP(&appcastDir, "dir", "d", ".", "project root directory")
	appcastCmd.Flags().StringVar(&appcastArtifacts, "artifacts", "", "artifacts directory (default: <dir>/artifacts)")
	appcastCmd.Flags().StringArrayVar(&appcastBaseURLs, "base-url", nil, "host serving the artifacts; repeat for each mirror (overrides package.update.base_url)")
}
