package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var iconCmd = &cobra.Command{
	Use:   "icon",
	Short: "Generate multi-size PNG icons and a Windows .ico from one source (.svg/.png)",
	RunE: func(cmd *cobra.Command, args []string) error {
		src, _ := cmd.Flags().GetString("source")
		out, _ := cmd.Flags().GetString("out")
		opts := runner.IconOptions{Source: src, OutDir: out}
		// Distinguish "flag absent → use default" from "flag passed → honor exactly";
		// IntSlice returns [] (not nil) when absent.
		if cmd.Flags().Changed("sizes") {
			opts.Sizes, _ = cmd.Flags().GetIntSlice("sizes")
		}
		if cmd.Flags().Changed("ico") {
			ico, _ := cmd.Flags().GetIntSlice("ico")
			if ico == nil {
				ico = []int{} // explicit "skip ICO"
			}
			opts.ICOSizes = ico
		}
		return runner.GenerateIcon(cmd.Context(), opts)
	},
}

func init() {
	rootCmd.AddCommand(iconCmd)
	iconCmd.Flags().StringP("source", "s", "", "source icon (.svg or .png) — required")
	iconCmd.Flags().StringP("out", "o", "", "output directory (default: source's directory)")
	iconCmd.Flags().IntSlice("sizes", nil, "PNG sizes to emit (default: 16,32,48,128,256,512,1024)")
	iconCmd.Flags().IntSlice("ico", nil, "sizes to bundle into icon.ico (default: 16,32,48,128,256; pass empty to skip)")
	_ = iconCmd.MarkFlagRequired("source")
}
