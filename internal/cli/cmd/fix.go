package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var fixCmd = &cobra.Command{
	Use:   "fix",
	Short: "Move the proto's @mcp, @cli, @event and @broadcast comments into scorix.v1 options",
	Long: "Rewrites the proto in place: each annotation comment becomes its (scorix.v1.mcp),\n" +
		"(scorix.v1.cli) or (scorix.v1.event) option, `destructive` becomes effect DESTRUCTIVE, and\n" +
		"the import is added. Notes name rpcs the option form treats as destructive where the comment\n" +
		"did not. --check writes nothing and exits 3 when there is something to move.",
	Args: cobra.NoArgs,
}

var (
	fixDir   string
	fixProto string
	fixCheck bool
)

func init() {
	rootCmd.AddCommand(fixCmd)
	fixCmd.Flags().StringVarP(&fixDir, "dir", "d", ".", "project root directory")
	fixCmd.Flags().StringVar(&fixProto, "proto", "", "fix this one proto instead of a project's, such as an idl repo's rpc.proto")
	fixCmd.Flags().BoolVar(&fixCheck, "check", false, "write nothing; exit 3 when there is something to move")
	jsonCommand(fixCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Fix(cmd.Context(), runner.FixOptions{Dir: fixDir, Proto: fixProto, Check: fixCheck, JSONOut: out})
	})
}
