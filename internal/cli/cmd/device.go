package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var deviceCmd = &cobra.Command{
	Use:   "device",
	Short: "Android devices for `scorix mobile dev`: list them, pair one over Wi-Fi, pick the default",
}

func deviceSub(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, opt runner.DeviceOptions) error) *cobra.Command {
	c := &cobra.Command{Use: use, Short: short, Args: args}
	jsonCommand(c, func(cmd *cobra.Command, out io.Writer) error {
		return run(cmd, runner.DeviceOptions{JSONOut: out, In: os.Stdin})
	})
	return c
}

func init() {
	listCmd := deviceSub("list", "Connected and remembered devices; * marks the default", cobra.NoArgs,
		func(cmd *cobra.Command, opt runner.DeviceOptions) error { return runner.DeviceList(cmd.Context(), opt) })

	pairCmd := deviceSub("pair [ip:port code]",
		"Pair over Wi-Fi debugging (Android 11+): scan the QR shown, or give the address and code the phone shows",
		cobra.MaximumNArgs(2),
		func(cmd *cobra.Command, opt runner.DeviceOptions) error {
			// One argument alone reaches the runner as an address with no code, which it refuses.
			opt.Address, opt.Code = cmd.Flags().Arg(0), cmd.Flags().Arg(1)
			return runner.DevicePair(cmd.Context(), opt)
		})

	useCmd := deviceSub("use <name|serial>", "Make a remembered device the default target", cobra.ExactArgs(1),
		func(cmd *cobra.Command, opt runner.DeviceOptions) error {
			opt.Device = cmd.Flags().Arg(0)
			return runner.DeviceUse(cmd.Context(), opt)
		})

	forgetCmd := deviceSub("forget <name|serial>", "Drop a remembered device and disconnect it", cobra.ExactArgs(1),
		func(cmd *cobra.Command, opt runner.DeviceOptions) error {
			opt.Device = cmd.Flags().Arg(0)
			return runner.DeviceForget(cmd.Context(), opt)
		})

	var runDir string
	runCmd := deviceSub("run [apk]", "Install an APK built elsewhere (Docker, CI) on the device, start it and follow its log", cobra.MaximumNArgs(1),
		func(cmd *cobra.Command, opt runner.DeviceOptions) error {
			opt.APK, opt.Dir = cmd.Flags().Arg(0), runDir
			opt.Device, _ = cmd.Flags().GetString("device")
			return runner.DeviceRun(cmd.Context(), opt)
		})
	runCmd.Flags().StringVarP(&runDir, "dir", "d", ".", "project root: its android/ names the app, its artifacts/ holds the APK")
	runCmd.Flags().StringP("device", "s", "", "adb serial or a `scorix device` name (default: as `mobile dev` picks)")

	deviceCmd.AddCommand(listCmd, pairCmd, useCmd, forgetCmd, runCmd)
	rootCmd.AddCommand(deviceCmd)
}
