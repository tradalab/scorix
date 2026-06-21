package cmd

import (
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
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.GenerateProto(cmd.Context(), runner.GenerateProtoOptions{
			Proto: generateProtoFile,
			Dir:   generateProtoDir,
			Force: generateProtoForce,
			Check: generateProtoCheck,
		})
	},
}

var (
	generateProtoFile  string
	generateProtoDir   string
	generateProtoForce bool
	generateProtoCheck bool
)

func init() {
	rootCmd.AddCommand(generateCmd)
	generateCmd.AddCommand(generateProtoCmd)

	generateProtoCmd.Flags().StringVarP(&generateProtoFile, "proto", "p", "idl/app.proto", "proto file path (overrides scorix.yaml proto:)")
	generateProtoCmd.Flags().StringVarP(&generateProtoDir, "dir", "d", ".", "project root directory")
	generateProtoCmd.Flags().BoolVarP(&generateProtoForce, "force", "f", false, "overwrite existing logic files")
	generateProtoCmd.Flags().BoolVar(&generateProtoCheck, "check", false, "verify generated code is in sync without writing (CI drift guard)")
}
