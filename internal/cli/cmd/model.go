package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var generateModelCmd = &cobra.Command{
	Use:   "model",
	Short: "Generate sqlx model and repository from SQL schema",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.GenerateModel(cmd.Context(), runner.GenerateModelOptions{
			Schema:  generateModelSchema,
			Dir:     generateModelDir,
			Force:   generateModelForce,
			Dialect: generateModelDialect,
			Check:   generateModelCheck,
		})
	},
}

var (
	generateModelSchema  string
	generateModelDir     string
	generateModelForce   bool
	generateModelDialect string
	generateModelCheck   bool
)

func init() {
	generateCmd.AddCommand(generateModelCmd)

	generateModelCmd.Flags().StringVarP(&generateModelSchema, "schema", "s", "etc/schema.sql", "SQL schema file path")
	generateModelCmd.Flags().StringVarP(&generateModelDir, "dir", "d", ".", "project root directory")
	generateModelCmd.Flags().BoolVarP(&generateModelForce, "force", "f", false, "overwrite existing implementation files")
	generateModelCmd.Flags().StringVar(&generateModelDialect, "dialect", "", "DB dialect: sqlite | mysql | postgres (default: scorix.yaml model.dialect, else sqlite)")
	generateModelCmd.Flags().BoolVar(&generateModelCheck, "check", false, "verify generated code is in sync without writing (CI drift guard)")
}
