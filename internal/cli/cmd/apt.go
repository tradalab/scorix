package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	aptDir       string
	aptArtifacts string
	aptURL       string
)

var aptCmd = &cobra.Command{
	Use:   "apt",
	Short: "Write a signed apt repository from the .deb artifacts",
	Long: "Write <artifacts>/apt: pool/, Packages and a Release signed as InRelease and\n" +
		"Release.gpg, for every .deb `scorix package --target deb` left in the artifacts\n" +
		"directory. Serve it at package.linux.apt.url and installed .deb copies update\n" +
		"through apt. The armored private key is read from SCORIX_APT_PRIVATE_KEY and\n" +
		"must match package.linux.apt.public_key.",
}

func init() {
	jsonCommand(aptCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Apt(cmd.Context(), runner.AptOptions{
			JSONOut:      out,
			Dir:          aptDir,
			ArtifactsDir: aptArtifacts,
			URL:          aptURL,
		})
	})
	rootCmd.AddCommand(aptCmd)
	aptCmd.Flags().StringVarP(&aptDir, "dir", "d", ".", "project root directory")
	aptCmd.Flags().StringVar(&aptArtifacts, "artifacts", "", "artifacts directory (default: <dir>/artifacts)")
	aptCmd.Flags().StringVar(&aptURL, "url", "", "where this repository will be served; refused unless it equals package.linux.apt.url")
}
