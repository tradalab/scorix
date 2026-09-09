package cmd

import (
	"os"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/mcp"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Serve the CLI to an agent over MCP (stdio)",
	Long: "Expose doctor, generate, build, package, appcast, init and the dev loop as MCP tools.\n" +
		"Each tool answers with the same JSON envelope as `scorix <command> --json`, so an agent\n" +
		"branches on ok/exit/kind instead of reading prose.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Here stdout IS the protocol, and the runner prints to it from dozens of
		// places. Take the real handle once, for the whole session rather than per
		// call, and hand the runner a pipe instead.
		transport := os.Stdout
		pr, pw, err := os.Pipe()
		if err != nil {
			return err
		}
		os.Stdout = pw
		defer func() {
			os.Stdout = transport
			pw.Close()
		}()

		s := mcp.NewServer(runner.Version().Short())
		// The pipe is read for two reasons at once: a human watching stderr still
		// sees progress, and a client that asked for it gets the same lines as
		// notifications instead of silence until the build ends.
		s.WatchOutput(pr, os.Stderr)
		return s.Serve(cmd.Context(), os.Stdin, transport)
	},
}

func init() { rootCmd.AddCommand(mcpCmd) }
