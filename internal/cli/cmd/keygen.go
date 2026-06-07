package cmd

import (
	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var keygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Generate an Ed25519 keypair for signing auto-update artifacts",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runner.GenerateKeypair()
	},
}

func init() {
	rootCmd.AddCommand(keygenCmd)
}
