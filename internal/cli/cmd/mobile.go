package cmd

import (
	"io"

	"github.com/spf13/cobra"
	"github.com/tradalab/scorix/internal/cli/runner"
)

var (
	mobileDir          string
	mobileABIs         []string
	mobileRelease      bool
	mobileBundle       bool
	mobileSkipFrontend bool
	mobileDevice       string
	mobileDevURL       string
)

var mobileCmd = &cobra.Command{
	Use:   "mobile",
	Short: "Build the app for Android (iOS later): the same Go main, inside a native project",
}

func mobileSub(use, short string, run func(cmd *cobra.Command, opt runner.MobileOptions) error) *cobra.Command {
	c := &cobra.Command{Use: use + " <platform>", Short: short, Args: cobra.ExactArgs(1)}
	jsonCommand(c, func(cmd *cobra.Command, out io.Writer) error {
		return run(cmd, runner.MobileOptions{
			JSONOut:      out,
			Dir:          mobileDir,
			Platform:     cmd.Flags().Arg(0),
			ABIs:         mobileABIs,
			Release:      mobileRelease,
			Bundle:       mobileBundle,
			SkipFrontend: mobileSkipFrontend,
			Device:       mobileDevice,
			DevURL:       mobileDevURL,
		})
	})
	c.Flags().StringVarP(&mobileDir, "dir", "d", ".", "project root directory")
	return c
}

func init() {
	initCmd := mobileSub("init", "Create the native project (android/) once; it is yours to edit from then on",
		func(cmd *cobra.Command, opt runner.MobileOptions) error { return runner.MobileInit(cmd.Context(), opt) })

	syncCmd := mobileSub("sync", "Rebuild the Go library per ABI (frontend embedded) and refresh the host library",
		func(cmd *cobra.Command, opt runner.MobileOptions) error { return runner.MobileSync(cmd.Context(), opt) })

	buildCmd := mobileSub("build", "Sync, then build an APK (debug, or --release) or an AAB (--release --aab) into artifacts/",
		func(cmd *cobra.Command, opt runner.MobileOptions) error {
			return runner.MobileBuild(cmd.Context(), opt)
		})
	buildCmd.Flags().BoolVar(&mobileRelease, "release", false, "release build, R8 on; signed when SCORIX_ANDROID_KEYSTORE and its password vars are set")
	buildCmd.Flags().BoolVar(&mobileBundle, "aab", false, "Android App Bundle for the store instead of an APK (needs --release)")

	devCmd := mobileSub("dev", "Build a debug APK, run it on a device or emulator and follow its log; HMR when the app has a shell",
		func(cmd *cobra.Command, opt runner.MobileOptions) error { return runner.MobileDev(cmd.Context(), opt) })
	devCmd.Flags().StringVarP(&mobileDevice, "device", "s", "", "adb serial or a `scorix device` name (default: the only one connected, else the default device)")
	devCmd.Flags().StringVar(&mobileDevURL, "url", "", "an already-running frontend dev server instead of starting `pnpm dev`")

	for _, c := range []*cobra.Command{syncCmd, buildCmd, devCmd} {
		c.Flags().StringSliceVar(&mobileABIs, "abi", nil, "ABIs to build (default arm64-v8a,x86_64)")
		c.Flags().BoolVar(&mobileSkipFrontend, "skip-frontend", false, "reuse existing .scorix/dist instead of rebuilding the frontend")
	}
	mobileCmd.AddCommand(initCmd, syncCmd, buildCmd, devCmd)
	rootCmd.AddCommand(mobileCmd)
}
