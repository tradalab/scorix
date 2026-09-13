package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var generateModelCmd = &cobra.Command{
	Use:   "model",
	Short: "Generate sqlx model and repository from SQL schema",
}

var (
	generateModelSchema  string
	generateModelDir     string
	generateModelForce   bool
	generateModelDialect string
	generateModelCheck   bool
)

func init() {
	jsonCommand(generateModelCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.GenerateModel(cmd.Context(), runner.GenerateModelOptions{
			JSONOut: out,
			Schema:  generateModelSchema,
			Dir:     generateModelDir,
			Force:   generateModelForce,
			Dialect: generateModelDialect,
			Check:   generateModelCheck,
		})
	})
	generateCmd.AddCommand(generateModelCmd)

	generateModelCmd.Flags().StringVarP(&generateModelSchema, "schema", "s", runner.DefaultSchemaPath, "SQL schema file path")
	generateModelCmd.Flags().StringVarP(&generateModelDir, "dir", "d", ".", "project root directory")
	generateModelCmd.Flags().BoolVarP(&generateModelForce, "force", "f", false, "overwrite existing implementation files")
	generateModelCmd.Flags().StringVar(&generateModelDialect, "dialect", "", "DB dialect: sqlite | mysql | postgres (default: scorix.yaml model.dialect, else sqlite)")
	generateModelCmd.Flags().BoolVar(&generateModelCheck, "check", false, "verify generated code is in sync without writing (CI drift guard)")
}
