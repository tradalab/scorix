package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Run the app's Go tests",
	Long: "Builds the embed directory first, so `//go:embed all:.scorix/dist` compiles without a\n" +
		"frontend, then runs go test. Failures come back as failures[] with the package, the test\n" +
		"name and its output; a compile error comes back as diagnostics[] like build.",
}

var lintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Run go vet over the app",
	Long:  "Same embed guarantee as test. Findings come back as findings[] with file, line and message.",
}

var (
	testDir  string
	testTags []string
	testRun  string
	testRace bool
	lintDir  string
	lintTags []string
)

func init() {
	rootCmd.AddCommand(testCmd)
	testCmd.Flags().StringVarP(&testDir, "dir", "d", ".", "project root directory")
	testCmd.Flags().StringSliceVar(&testTags, "tags", nil, "extra build tags (scorix.yaml build.tags always apply)")
	testCmd.Flags().StringVar(&testRun, "run", "", "only run tests matching this pattern")
	testCmd.Flags().BoolVar(&testRace, "race", false, "enable the race detector")
	jsonCommand(testCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Test(cmd.Context(), runner.TestOptions{
			JSONOut: out, Dir: testDir, Tags: testTags, Run: testRun, Race: testRace,
		})
	})

	rootCmd.AddCommand(lintCmd)
	lintCmd.Flags().StringVarP(&lintDir, "dir", "d", ".", "project root directory")
	lintCmd.Flags().StringSliceVar(&lintTags, "tags", nil, "extra build tags (scorix.yaml build.tags always apply)")
	jsonCommand(lintCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Lint(cmd.Context(), runner.LintOptions{JSONOut: out, Dir: lintDir, Tags: lintTags})
	})
}
