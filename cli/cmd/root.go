package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	cfgFile string
)

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "scorix",
	Short: "Scorix CLI – build native apps with Go + WebUI",
	Long:  "Scorix CLI.\nBuild, scaffold and manage Scorix applications.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Global init
		ctx := context.Background()
		cmd.SetContext(ctx)
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config path (optional)")
}
