package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The host library is recopied from go.mod's scorix on every sync because the
// JNI contract between it and the app's c-shared lib has no version of its own.
const (
	androidMinSDK        = 24
	androidActivity      = "dev.scorix.ScorixActivity"
	androidGradleVersion = "8.10.2" // the wrapper init writes; AGP 8.7 needs 8.9 or newer
	androidLibName       = "libscorixapp.so"
	scorixModule         = "github.com/tradalab/scorix"
)

type androidABI struct {
	Name   string // jniLibs directory and Android's own name for it
	GOARCH string
	GOARM  string
	Triple string // NDK clang prefix, before the API level
}

var androidABIs = []androidABI{
	{Name: "arm64-v8a", GOARCH: "arm64", Triple: "aarch64-linux-android"},
	{Name: "x86_64", GOARCH: "amd64", Triple: "x86_64-linux-android"},
	{Name: "armeabi-v7a", GOARCH: "arm", GOARM: "7", Triple: "armv7a-linux-androideabi"},
	{Name: "x86", GOARCH: "386", Triple: "i686-linux-android"},
}

// Phones are arm64, PC emulators x86_64; the 32-bit pair would cost two more Go builds.
var defaultAndroidABIs = []string{"arm64-v8a", "x86_64"}

type MobileOptions struct {
	Dir          string
	Platform     string
	ABIs         []string
	Release      bool
	Bundle       bool // AAB instead of APK; release only
	SkipFrontend bool
	Device       string // adb serial or a remembered device; empty: the only one, else the default
	DevURL       string // dev only: an already-running frontend dev server
	JSONOut      io.Writer
}

type MobileResult struct {
	Platform  string   `json:"platform"`
	Project   string   `json:"project,omitempty"`
	Libraries []string `json:"libraries,omitempty"`
	Output    string   `json:"output,omitempty"`
	Signed    *bool    `json:"signed,omitempty"`
	AppID     string   `json:"appId,omitempty"`
}

func MobileInit(ctx context.Context, opt MobileOptions) error {
	return mobileRun(opt, "mobile init", func(res *MobileResult) error { return mobileInit(ctx, opt, res) })
}

func MobileSync(ctx context.Context, opt MobileOptions) error {
	return mobileRun(opt, "mobile sync", func(res *MobileResult) error {
		_, err := mobileSync(ctx, opt, res)
		return err
	})
}

func MobileBuild(ctx context.Context, opt MobileOptions) error {
	return mobileRun(opt, "mobile build", func(res *MobileResult) error { return mobileBuild(ctx, opt, res) })
}

func MobileDev(ctx context.Context, opt MobileOptions) error {
	return mobileRun(opt, "mobile dev", func(res *MobileResult) error { return mobileDev(ctx, opt, res) })
}

func mobileRun(opt MobileOptions, command string, run func(*MobileResult) error) error {
	res := &MobileResult{Platform: opt.Platform}
	err := checkMobilePlatform(opt.Platform)
	if err == nil {
		err = run(res)
	}
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, command, res, err)
	}
	return err
}

func checkMobilePlatform(p string) error {
	switch p {
	case "android":
		return nil
	case "ios":
		return exitErrorf(ExitUsage, "usage", "ios is not supported yet: it needs a Mac and comes after Android")
	}
	return UsageError(fmt.Errorf("unknown mobile platform %q (want android)", p))
}

func mobileInit(ctx context.Context, opt MobileOptions, res *MobileResult) error {
	bc, err := resolveBuildContext(opt.Dir, "android", "arm64")
	if err != nil {
		return err
	}
	dir := filepath.Join(bc.Root, "android")
	res.Project = dir
	// Never rewritten: the app owns these files after init and a rerun would undo its edits.
	if _, err := os.Stat(dir); err == nil {
		return exitErrorf(ExitFailed, "exists", "%s already exists and belongs to the app now; delete it to start over", dir)
	}
	appID, err := androidAppID(bc.Identifier)
	if err != nil {
		return err
	}
	res.AppID = appID
	data := map[string]any{
		"Name":        bc.AppName,
		"Label":       bc.ProductName,
		"AppID":       appID,
		"Version":     bc.Version,
		"VersionCode": androidVersionCode(bc.Version),
	}
	fmt.Printf("==> Creating Android project in %s\n", dir)
	if err := writeTemplateFS("static/mobile/android", dir, data); err != nil {
		return err
	}
	gradle, err := exec.LookPath("gradle")
	if err != nil {
		fmt.Println("warning: gradle not found - no wrapper written; install Gradle and run `gradle wrapper` in android/, or build with Android Studio")
		return nil
	}
	fmt.Printf("==> gradle wrapper --gradle-version %s\n", androidGradleVersion)
	cmd := exec.CommandContext(ctx, gradle, "wrapper", "--gradle-version", androidGradleVersion)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gradle wrapper: %w", toolchainError(err, stderr.String()))
	}
	return nil
}

func mobileSync(ctx context.Context, opt MobileOptions, res *MobileResult) (*BuildContext, error) {
	bc, err := resolveBuildContext(opt.Dir, "android", "arm64")
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(bc.Root, "android")
	res.Project = dir
	if _, err := os.Stat(filepath.Join(dir, "settings.gradle.kts")); err != nil {
		return nil, exitErrorf(ExitFailed, "not_initialized", "no Android project in %s - run `scorix mobile init android` first", dir)
	}
	abis, err := selectAndroidABIs(opt.ABIs)
	if err != nil {
		return nil, err
	}
	sdk, err := findAndroidSDK()
	if err != nil {
		return nil, err
	}
	ndk, err := findAndroidNDK(sdk)
	if err != nil {
		return nil, err
	}

	modDir, err := scorixModuleDir(ctx, bc.Root)
	if err != nil {
		return nil, err
	}
	host := filepath.Join(modDir, "mobile", "android", "scorix")
	if _, err := os.Stat(filepath.Join(host, "build.gradle.kts")); err != nil {
		return nil, fmt.Errorf("the scorix in go.mod (%s) has no Android host library - it needs a version with Android support", modDir)
	}
	hostDst := filepath.Join(dir, ".scorix", "host")
	fmt.Printf("==> Copying host library %s -> %s\n", host, hostDst)
	if err := os.RemoveAll(hostDst); err != nil {
		return nil, err
	}
	if err := copySourceTree(host, hostDst); err != nil {
		return nil, err
	}

	if err := writeAndroidVersion(dir, bc.Version); err != nil {
		return nil, err
	}

	if opt.SkipFrontend {
		fmt.Println("==> Skipping frontend build (using existing .scorix/dist)")
	} else if err := buildFrontend(ctx, bc); err != nil {
		return nil, err
	}
	if err := ensureDist(bc); err != nil {
		return nil, err
	}
	warnUnpinnedAssets(bc.DistDir)

	// Cleared first: a lib left from a wider ABI list would ship a stale build in the APK.
	jni := filepath.Join(dir, "app", "src", "main", "jniLibs")
	if err := os.RemoveAll(jni); err != nil {
		return nil, err
	}
	for _, abi := range abis {
		out := filepath.Join(jni, abi.Name, androidLibName)
		if err := goBuildAndroid(ctx, bc, ndk, abi, out); err != nil {
			return nil, err
		}
		res.Libraries = append(res.Libraries, out)
	}
	return bc, nil
}

func goBuildAndroid(ctx context.Context, bc *BuildContext, ndk string, abi androidABI, out string) error {
	cc, err := ndkClang(ndk, abi)
	if err != nil {
		return err
	}
	// Always stripped: size is paid per install, and Go keeps its own symbols for panic traces.
	ld := append([]string{"-s", "-w"}, bc.Ldflags...)
	args := []string{"build", "-buildmode=c-shared", "-trimpath", "-ldflags", strings.Join(ld, " ")}
	if len(bc.Tags) > 0 {
		args = append(args, "-tags", strings.Join(bc.Tags, ","))
	}
	args = append(args, "-o", out, ".")
	// Play requires 16 KB pages. CGO_LDFLAGS, not -extldflags: an app's own -extldflags would replace it.
	cgoLD := strings.TrimSpace(os.Getenv("CGO_LDFLAGS") + " -Wl,-z,max-page-size=16384")
	env := append(os.Environ(), "GOOS=android", "GOARCH="+abi.GOARCH, "CGO_ENABLED=1", "CC="+cc, "CGO_LDFLAGS="+cgoLD)
	if abi.GOARM != "" {
		env = append(env, "GOARM="+abi.GOARM)
	}
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = bc.Root
	cmd.Env = env
	cmd.Stdout = os.Stdout
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	fmt.Printf("==> [%s] GOOS=android GOARCH=%s go %s\n", abi.Name, abi.GOARCH, strings.Join(args, " "))
	if err := cmd.Run(); err != nil {
		return toolchainError(err, stderr.String())
	}
	// c-shared leaves a C header beside the lib when main exports anything; Gradle would pack it.
	if err := os.Remove(strings.TrimSuffix(out, ".so") + ".h"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func mobileBuild(ctx context.Context, opt MobileOptions, res *MobileResult) error {
	if opt.Bundle && !opt.Release {
		return UsageError(errors.New("--aab needs --release: an app bundle is what goes to the store"))
	}
	bc, err := mobileSync(ctx, opt, res)
	if err != nil {
		return err
	}
	dir := res.Project
	out, err := gradleAssemble(ctx, dir, opt)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%s-%s-android", bc.AppName, bc.Version)
	if !opt.Release {
		name += "-debug"
	} else {
		signed := os.Getenv("SCORIX_ANDROID_KEYSTORE") != ""
		res.Signed = &signed
		// Named apart so an unsigned build never overwrites, and passes for, a signed one.
		if !signed {
			name += "-unsigned"
			fmt.Println("warning: release build is unsigned - set SCORIX_ANDROID_KEYSTORE, SCORIX_ANDROID_KEYSTORE_PASSWORD, SCORIX_ANDROID_KEY_ALIAS and SCORIX_ANDROID_KEY_PASSWORD to sign it")
		}
	}
	dst := filepath.Join(bc.ArtifactDir, name+filepath.Ext(out))
	if err := copyFile(out, dst); err != nil {
		return err
	}
	res.Output = dst
	fmt.Printf("==> Built %s\n", dst)
	return nil
}

func gradleAssemble(ctx context.Context, dir string, opt MobileOptions) (string, error) {
	gradle, err := gradleCommand(dir)
	if err != nil {
		return "", err
	}
	task, out := ":app:assembleDebug", filepath.Join("app", "build", "outputs", "apk", "debug", "app-debug.apk")
	switch {
	case opt.Bundle:
		task, out = ":app:bundleRelease", filepath.Join("app", "build", "outputs", "bundle", "release", "app-release.aab")
	case opt.Release:
		task, out = ":app:assembleRelease", filepath.Join("app", "build", "outputs", "apk", "release", "app-release.apk")
		if os.Getenv("SCORIX_ANDROID_KEYSTORE") == "" {
			out = filepath.Join("app", "build", "outputs", "apk", "release", "app-release-unsigned.apk")
		}
	}
	cmd := exec.CommandContext(ctx, gradle, task)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	if sdk, err := findAndroidSDK(); err == nil && os.Getenv("ANDROID_HOME") == "" {
		cmd.Env = append(cmd.Env, "ANDROID_HOME="+sdk)
	}
	cmd.Stdout = os.Stdout
	var stderr strings.Builder
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	fmt.Printf("==> %s %s\n", filepath.Base(gradle), task)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("gradle %s: %w", task, toolchainError(err, stderr.String()))
	}
	return filepath.Join(dir, out), nil
}

// The project's own wrapper wins: it pins the Gradle the project was made for.
func gradleCommand(dir string) (string, error) {
	wrapper := "gradlew"
	if runtime.GOOS == "windows" {
		wrapper = "gradlew.bat"
	}
	if p := filepath.Join(dir, wrapper); fileExists(p) {
		return p, nil
	}
	if p, err := exec.LookPath("gradle"); err == nil {
		return p, nil
	}
	return "", exitErrorf(ExitMissing, "missing_prerequisite", "no gradlew in %s and no gradle in PATH - install Gradle >= %s, then `gradle wrapper` there", dir, androidGradleVersion)
}

func mobileDev(ctx context.Context, opt MobileOptions, res *MobileResult) error {
	opt.Release, opt.Bundle = false, false
	adbRun, err := systemADB()
	if err != nil {
		return err
	}
	target, err := resolveDevice(ctx, adbRun, opt.Device)
	if err != nil {
		return err
	}
	opt.Device = target.Serial
	// One device runs one ABI; building the others too costs a Go cross-compile each.
	if len(opt.ABIs) == 0 && slices.ContainsFunc(androidABIs, func(a androidABI) bool { return a.Name == target.ABI }) {
		opt.ABIs = []string{target.ABI}
	}
	fmt.Printf("==> Device %s (%s, %s)\n", target.Name, target.Serial, target.ABI)
	if err := mobileBuild(ctx, opt, res); err != nil {
		return err
	}
	dev, err := deviceCmd(ctx, opt.Device)
	if err != nil {
		return err
	}
	appID, err := androidProjectAppID(res.Project)
	if err != nil {
		return err
	}
	res.AppID = appID
	if err := installAPK(dev, res.Output, appID); err != nil {
		return err
	}

	devURL := opt.DevURL
	root := filepath.Dir(res.Project)
	if devURL == "" && fileExists(filepath.Join(root, "shell", "package.json")) {
		port := devServerPort(filepath.Join(root, "scorix.yaml"))
		fmt.Printf("==> Starting shell dev server (pnpm dev) on port %d...\n", port)
		srv := exec.CommandContext(ctx, "pnpm", "dev")
		srv.Dir = filepath.Join(root, "shell")
		srv.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
		srv.Stdout, srv.Stderr = os.Stdout, os.Stderr
		if err := srv.Start(); err != nil {
			return fmt.Errorf("start shell dev server: %w", err)
		}
		defer func() { _ = srv.Process.Kill() }()
		devURL = fmt.Sprintf("http://localhost:%d", port)
		if err := waitForServer(ctx, devURL, 60*time.Second); err != nil {
			return err
		}
	}
	start := []string{"shell", "am", "start", "-n", appID + "/" + androidActivity}
	if devURL != "" {
		// localhost on the device is the device; reverse makes it this machine.
		port := devURL[strings.LastIndexByte(devURL, ':')+1:]
		port = strings.TrimRight(port, "/")
		if err := dev("reverse", "tcp:"+port, "tcp:"+port).Run(); err != nil {
			return fmt.Errorf("adb reverse: %w", err)
		}
		start = append(start, "--es", "scorix.devUrl", devURL)
		fmt.Printf("==> Frontend from %s (HMR)\n", devURL)
	}
	return startAndFollow(dev, appID, start)
}

func deviceCmd(ctx context.Context, serial string) (func(args ...string) *exec.Cmd, error) {
	adb, err := findADB()
	if err != nil {
		return nil, err
	}
	return func(args ...string) *exec.Cmd {
		if serial != "" {
			args = append([]string{"-s", serial}, args...)
		}
		c := exec.CommandContext(ctx, adb, args...)
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		return c
	}, nil
}

func installAPK(dev func(args ...string) *exec.Cmd, apk, appID string) error {
	fmt.Printf("==> adb install -r %s\n", apk)
	install := dev("install", "-r", apk)
	var installOut strings.Builder
	install.Stdout = io.MultiWriter(os.Stdout, &installOut)
	install.Stderr = io.MultiWriter(os.Stderr, &installOut)
	if err := install.Run(); err != nil {
		// Not uninstalled for the user: that wipes app data they may be testing with.
		if strings.Contains(installOut.String(), "INSTALL_FAILED_UPDATE_INCOMPATIBLE") {
			return fmt.Errorf("%s is already installed with another signing key (a release build?) - `adb uninstall %s` removes it and its data, then run again", appID, appID)
		}
		return fmt.Errorf("adb install: %w", toolchainError(err, installOut.String()))
	}
	return nil
}

func startAndFollow(dev func(args ...string) *exec.Cmd, appID string, start []string) error {
	_ = dev("shell", "am", "force-stop", appID).Run()
	if err := dev(start...).Run(); err != nil {
		return fmt.Errorf("adb am start: %w", err)
	}

	var pid string
	for i := 0; i < 50 && pid == ""; i++ {
		c := dev("shell", "pidof", appID)
		c.Stdout = nil
		b, _ := c.Output()
		pid = strings.TrimSpace(string(b))
		if pid == "" {
			time.Sleep(200 * time.Millisecond)
		}
	}
	if pid == "" {
		return fmt.Errorf("%s did not start - see `adb logcat -b crash`", appID)
	}
	fmt.Printf("==> Following the log of %s (pid %s); Ctrl+C to stop\n", appID, pid)
	return dev("logcat", "--pid="+pid, "-v", "time").Run()
}

func selectAndroidABIs(names []string) ([]androidABI, error) {
	if len(names) == 0 {
		names = defaultAndroidABIs
	}
	var out []androidABI
	for _, n := range names {
		found := false
		for _, a := range androidABIs {
			if a.Name == n {
				out = append(out, a)
				found = true
				break
			}
		}
		if !found {
			return nil, UsageError(fmt.Errorf("unknown Android ABI %q (want arm64-v8a, x86_64, armeabi-v7a or x86)", n))
		}
	}
	return out, nil
}

// app.identifier is usually close to a Java package name (com.example.my-app),
// so dashes become underscores rather than an error.
func androidAppID(identifier string) (string, error) {
	if identifier == "" {
		return "", fmt.Errorf("app.identifier is empty in scorix.yaml - Android needs it as the application ID (e.g. com.example.myapp)")
	}
	id := strings.ReplaceAll(identifier, "-", "_")
	segs := strings.Split(id, ".")
	valid := len(segs) >= 2
	for _, s := range segs {
		if !javaSegment.MatchString(s) {
			valid = false
		}
	}
	if !valid {
		return "", fmt.Errorf("app.identifier %q cannot be an Android application ID: it needs two or more dot-separated parts, each starting with a letter", identifier)
	}
	return id, nil
}

// Written on every sync, not into build.gradle.kts at init: the app owns that file,
// so a version bump would keep the old versionCode, which the store rejects.
func writeAndroidVersion(dir, version string) error {
	p := filepath.Join(dir, ".scorix", "version.properties")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf("versionName=%s\nversionCode=%d\n", version, androidVersionCode(version))
	return os.WriteFile(p, []byte(body), 0o644)
}

var javaSegment = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

// MAJOR*1e6 + MINOR*1e3 + PATCH keeps the order of the version and stays under
// Play's 2.1e9 ceiling up to major 2100; anything unparsable starts at 1.
func androidVersionCode(version string) int {
	core := strings.SplitN(strings.TrimPrefix(version, "v"), "-", 2)[0]
	parts := strings.Split(core, ".")
	code := 0
	for i, mul := range []int{1_000_000, 1_000, 1} {
		if i >= len(parts) {
			break
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 || (i > 0 && n >= 1000) {
			return 1
		}
		code += n * mul
	}
	return max(code, 1)
}

var gradleAppID = regexp.MustCompile(`applicationId\s*=\s*"([^"]+)"`)

// Read back, not derived again: the app may have changed the ID since init.
func androidProjectAppID(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "app", "build.gradle.kts"))
	if err != nil {
		return "", err
	}
	m := gradleAppID.FindSubmatch(b)
	if m == nil {
		return "", fmt.Errorf("no applicationId in %s", filepath.Join(dir, "app", "build.gradle.kts"))
	}
	return string(m[1]), nil
}

func findAndroidSDK() (string, error) {
	cands := []string{os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT")}
	if home, err := os.UserHomeDir(); err == nil {
		switch runtime.GOOS {
		case "darwin":
			cands = append(cands, filepath.Join(home, "Library", "Android", "sdk"))
		case "windows":
			cands = append(cands, filepath.Join(os.Getenv("LOCALAPPDATA"), "Android", "Sdk"))
		default:
			cands = append(cands, filepath.Join(home, "Android", "Sdk"))
		}
	}
	if p := firstExisting(cands...); p != "" {
		return p, nil
	}
	return "", exitErrorf(ExitMissing, "missing_prerequisite", "Android SDK not found - install it (Android Studio or cmdline-tools) and set ANDROID_HOME")
}

func findAndroidNDK(sdk string) (string, error) {
	for _, env := range []string{"ANDROID_NDK_HOME", "ANDROID_NDK_ROOT"} {
		if p := os.Getenv(env); p != "" && fileExists(filepath.Join(p, "toolchains", "llvm")) {
			return p, nil
		}
	}
	if p := newestNDK(filepath.Join(sdk, "ndk")); p != "" {
		return p, nil
	}
	return "", exitErrorf(ExitMissing, "missing_prerequisite", "Android NDK not found in %s - `sdkmanager \"ndk;27.2.12479018\"` or set ANDROID_NDK_HOME", filepath.Join(sdk, "ndk"))
}

func newestNDK(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var vers []string
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(dir, e.Name(), "toolchains", "llvm")) {
			vers = append(vers, e.Name())
		}
	}
	if len(vers) == 0 {
		return ""
	}
	sort.Slice(vers, func(i, j int) bool { return versionLess(vers[i], vers[j]) })
	return filepath.Join(dir, vers[len(vers)-1])
}

// Numeric per part: a string sort puts 27.10.1 before 27.2.12479018.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		if ea != nil || eb != nil {
			if pa[i] != pb[i] {
				return pa[i] < pb[i]
			}
			continue
		}
		if na != nb {
			return na < nb
		}
	}
	return len(pa) < len(pb)
}

func ndkClang(ndk string, abi androidABI) (string, error) {
	host := map[string]string{"linux": "linux-x86_64", "darwin": "darwin-x86_64", "windows": "windows-x86_64"}[runtime.GOOS]
	if host == "" {
		return "", fmt.Errorf("the Android NDK has no toolchain for %s", runtime.GOOS)
	}
	name := fmt.Sprintf("%s%d-clang", abi.Triple, androidMinSDK)
	if runtime.GOOS == "windows" {
		name += ".cmd"
	}
	p := filepath.Join(ndk, "toolchains", "llvm", "prebuilt", host, "bin", name)
	if !fileExists(p) {
		return "", exitErrorf(ExitMissing, "missing_prerequisite", "NDK compiler %s not found - the NDK at %s is incomplete or too old", name, ndk)
	}
	return p, nil
}

func findADB() (string, error) {
	if sdk, err := findAndroidSDK(); err == nil {
		name := "adb"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if p := filepath.Join(sdk, "platform-tools", name); fileExists(p) {
			return p, nil
		}
	}
	if p, err := exec.LookPath("adb"); err == nil {
		return p, nil
	}
	return "", exitErrorf(ExitMissing, "missing_prerequisite", "adb not found - install the SDK platform-tools")
}

func scorixModuleDir(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{.Dir}}", scorixModule)
	cmd.Dir = root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("locate %s from go.mod: %w", scorixModule, toolchainError(err, stderr.String()))
	}
	dir := strings.TrimSpace(string(b))
	if dir == "" {
		return "", fmt.Errorf("%s is not downloaded yet - run `go mod download` in %s", scorixModule, root)
	}
	return dir, nil
}

// Skips build/ and caches, which a development checkout carries next to the source.
func copySourceTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != src {
			switch d.Name() {
			case "build", ".gradle", ".kotlin", ".cxx":
				return filepath.SkipDir
			}
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}
