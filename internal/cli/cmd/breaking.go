package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var breakingCmd = &cobra.Command{
	Use:   "breaking --against OLD NEW",
	Short: "Report what a new version of a proto breaks for callers of the old one",
	Long: "Compares two versions of a proto at three levels: wire (protobuf binary callers such as\n" +
		"zrpc and gRPC), json (Connect JSON, REST and the TS client) and contract (scorix.v1\n" +
		"routes, effects, permissions, paging, MCP tools and CLI commands). Each level also\n" +
		"reports the ones before it. OLD is a file or what git show reads: REV:PATH from the\n" +
		"repository root, or REV:./PATH from here, such as main:./idl/app.proto.\n" +
		"Any change at --level or below exits 5.",
	Args: cobra.ExactArgs(1),
}

var (
	breakingAgainst string
	breakingLevel   string
)

func init() {
	rootCmd.AddCommand(breakingCmd)
	breakingCmd.Flags().StringVar(&breakingAgainst, "against", "", "the old proto: a file, or REV:PATH read with git show")
	breakingCmd.Flags().StringVar(&breakingLevel, "level", "contract", "wire, json or contract")
	breakingCmd.MarkFlagRequired("against")
	jsonCommand(breakingCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Breaking(cmd.Context(), runner.BreakingOptions{
			Before: breakingAgainst, After: cmd.Flags().Arg(0), Level: breakingLevel, JSONOut: out,
		})
	})
}
