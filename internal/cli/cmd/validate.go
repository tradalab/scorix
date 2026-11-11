package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var validateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Check the files codegen reads before generating from them",
	Long: "Reads scorix.yaml, the proto and the SQL schema and reports what is wrong with them\n" +
		"without writing anything. Errors stop generate; warnings mean it will run and produce\n" +
		"something you probably did not intend, or that the proto strays from a Google AIP.",
}

var (
	validateDir   string
	validateProto string
)

func init() {
	rootCmd.AddCommand(validateCmd)
	validateCmd.Flags().StringVarP(&validateDir, "dir", "d", ".", "project root directory")
	validateCmd.Flags().StringVar(&validateProto, "proto", "", "validate and lint this one proto instead of a project, such as an idl repo's rpc.proto")
	jsonCommand(validateCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Validate(cmd.Context(), runner.ValidateOptions{JSONOut: out, Dir: validateDir, Proto: validateProto})
	})
}
