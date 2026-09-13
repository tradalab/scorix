package cmd

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	surfaceDir   string
	surfaceProto string
)

var surfaceCmd = &cobra.Command{
	Use:   "surface",
	Short: "Print the IPC surface the proto declares, without writing anything",
}

func init() {
	rootCmd.AddCommand(surfaceCmd)
	surfaceCmd.Flags().StringVarP(&surfaceDir, "dir", "d", ".", "project root directory")
	surfaceCmd.Flags().StringVarP(&surfaceProto, "proto", "p", "idl/app.proto", "proto path (overridden by scorix.yaml proto:)")
	jsonCommand(surfaceCmd, func(cmd *cobra.Command, out io.Writer) error {
		return runner.Surface(cmd.Context(), runner.SurfaceOptions{
			Dir:     surfaceDir,
			Proto:   surfaceProto,
			JSONOut: out,
		})
	})
}
