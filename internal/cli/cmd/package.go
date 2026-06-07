package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	packageDir          string
	packageOS           string
	packageArch         string
	packageFormat       string
	packageTags         []string
	packageSkipFrontend bool
	packageSkipSign     bool
	packageSign         bool
)

var packageCmd = &cobra.Command{
	Use:     "package",
	Aliases: []string{"pkg"},
	Short:   "Build and package the app into a native installer (msi/dmg/appimage)",
	Long: "Build and package the app into a native installer.\n\n" +
		"Native installers require their target OS toolchain (WiX for MSI, etc.),\n" +
		"so by default packaging targets the host OS only. Currently implemented: windows (MSI).",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.Package(cmd.Context(), runner.PackageOptions{
			Dir:          packageDir,
			OS:           packageOS,
			Arch:         packageArch,
			Format:       packageFormat,
			Tags:         packageTags,
			SkipFrontend: packageSkipFrontend,
			SkipSign:     packageSkipSign,
			ForceSign:    packageSign,
		})
	},
}

func init() {
	rootCmd.AddCommand(packageCmd)
	packageCmd.Flags().StringVarP(&packageDir, "dir", "d", ".", "project root directory")
	packageCmd.Flags().StringVar(&packageOS, "os", "", "target GOOS (default: host / scorix.yaml targets)")
	packageCmd.Flags().StringVar(&packageArch, "arch", "", "target GOARCH (default: host)")
	packageCmd.Flags().StringVar(&packageFormat, "target", "", "installer format (msi|dmg|appimage); default per-OS")
	packageCmd.Flags().StringSliceVar(&packageTags, "tags", nil, "extra go build tags")
	packageCmd.Flags().BoolVar(&packageSkipFrontend, "skip-frontend", false, "reuse existing .scorix/dist instead of rebuilding the frontend")
	packageCmd.Flags().BoolVar(&packageSkipSign, "skip-sign", false, "skip code signing / notarization even if configured")
	packageCmd.Flags().BoolVar(&packageSign, "sign", false, "force code signing on (CI opt-in) even if package.sign.<os>.enabled is false; requires the cert env to be set")
}
