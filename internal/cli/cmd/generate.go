package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var generateCmd = &cobra.Command{
	Use:     "generate",
	Aliases: []string{"gen"},
	Short:   "Generate Scorix application code",
}

var generateProtoCmd = &cobra.Command{
	Use:     "proto",
	Aliases: []string{"rpc"},
	Short:   "Generate handler, logic and types from a proto file",
}

var (
	generateProtoFile  string
	generateProtoDir   string
	generateProtoForce bool
	generateProtoCheck bool
)

func init() {
	jsonCommand(generateProtoCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.GenerateProto(cmd.Context(), runner.GenerateProtoOptions{
			JSONOut: out,
			Proto:   generateProtoFile,
			Dir:     generateProtoDir,
			Force:   generateProtoForce,
			Check:   generateProtoCheck,
		})
	})
	rootCmd.AddCommand(generateCmd)
	generateCmd.AddCommand(generateProtoCmd)

	generateProtoCmd.Flags().StringVarP(&generateProtoFile, "proto", "p", "idl/app.proto", "proto file path (overrides scorix.yaml proto:)")
	generateProtoCmd.Flags().StringVarP(&generateProtoDir, "dir", "d", ".", "project root directory")
	generateProtoCmd.Flags().BoolVarP(&generateProtoForce, "force", "f", false, "overwrite existing logic files")
	generateProtoCmd.Flags().BoolVar(&generateProtoCheck, "check", false, "verify generated code is in sync without writing (CI drift guard)")
}
