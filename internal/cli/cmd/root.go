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
	// Runtime failures (drift, parse error) aren't usage mistakes; Execute already prints the error.
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cmd.SetContext(context.Background())
		return nil
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config path (optional)")
}
