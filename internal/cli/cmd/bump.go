package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var bumpCmd = &cobra.Command{
	Use:   "bump [version]",
	Short: "Move an app to a scorix version: every pin, then the checks",
	Long: "Run inside an app repo. Rewrites the scorix version in go.mod, the go directive,\n" +
		"the reusable-workflow refs and any `go install .../cmd/scorix@vX`, regenerates, then\n" +
		"runs the checks the release rule asks for. `upgrade` installs the CLI on this machine;\n" +
		"`bump` moves a repo, and refuses unless the CLI already is the target version.\n\n" +
		"--check writes nothing and needs no network: it answers whether the pins agree with\n" +
		"each other and with this CLI, and - when given a version - whether they are on it.",
	Args: cobra.MaximumNArgs(1),
}

var (
	bumpDir   string
	bumpCheck bool
)

func init() {
	rootCmd.AddCommand(bumpCmd)
	bumpCmd.Flags().StringVarP(&bumpDir, "dir", "d", ".", "project root directory")
	bumpCmd.Flags().BoolVar(&bumpCheck, "check", false, "report whether the pins agree, and match [version] if given; write nothing")
	jsonCommand(bumpCmd, func(cmd *cobra.Command, out io.Writer) error {
		var version string
		if args := cmd.Flags().Args(); len(args) == 1 {
			version = args[0]
		}
		return runner.Bump(cmd.Context(), runner.BumpOptions{
			Dir: bumpDir, Version: version, Check: bumpCheck, JSONOut: out,
		})
	})
}
