package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	buildDir          string
	buildOS           string
	buildArch         string
	buildOutput       string
	buildTags         []string
	buildSkipFrontend bool
)

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Compile the app (frontend + Go) into a single binary for a target OS/arch",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Build(cmd.Context(), runner.BuildOptions{
			Dir:          buildDir,
			OS:           buildOS,
			Arch:         buildArch,
			Output:       buildOutput,
			Tags:         buildTags,
			SkipFrontend: buildSkipFrontend,
		})
	},
}

func init() {
	rootCmd.AddCommand(buildCmd)
	buildCmd.Flags().StringVarP(&buildDir, "dir", "d", ".", "project root directory")
	buildCmd.Flags().StringVar(&buildOS, "os", "", "target GOOS (default: host)")
	buildCmd.Flags().StringVar(&buildArch, "arch", "", "target GOARCH (default: host)")
	buildCmd.Flags().StringVarP(&buildOutput, "output", "o", "", "output binary path (default: .scorix/<name>-<ver>-<os>-<arch>/<name>)")
	buildCmd.Flags().StringSliceVar(&buildTags, "tags", nil, "extra go build tags")
	buildCmd.Flags().BoolVar(&buildSkipFrontend, "skip-frontend", false, "reuse existing .scorix/dist instead of rebuilding the frontend")
}
