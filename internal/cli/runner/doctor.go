package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/tradalab/scorix/diag"
	"github.com/tradalab/scorix/module"
)

const (
	minGoMajor = 1
	minGoMinor = 27
)

type DoctorOptions struct {
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | error
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type DoctorResult struct {
	OS       string        `json:"os"`
	Arch     string        `json:"arch"`
	Checks   []DoctorCheck `json:"checks"`
	Warnings int           `json:"warnings"`
}

// Recording and printing come from one call so a new check cannot land in only
// one of them. "error" prints nothing: it comes back as the command's error and
// Execute already puts that on stderr - printing here said it twice.
func (r *DoctorResult) add(c DoctorCheck) {
	r.Checks = append(r.Checks, c)
	switch c.Status {
	case "ok":
		fmt.Printf("OK: %s\n", strings.TrimSpace(c.Name+" "+c.Detail))
	case "warn":
		r.Warnings++
		fmt.Printf("WARN: %s\n", c.Hint)
	}
}

func Doctor(ctx context.Context, opt DoctorOptions) error {
	fmt.Println("Checking toolchain...")
	res := &DoctorResult{OS: runtime.GOOS, Arch: runtime.GOARCH}
	err := doctorChecks(ctx, res)
	if opt.JSONOut != nil {
		return EmitJSON(opt.JSONOut, "doctor", res, err)
	}
	return err
}

// doctorChecks fails only on a missing Go toolchain: everything else is a soft
// tool whose absence blocks one command, not the CLI, so it stays a warning and
// keeps the exit status at 0 for CI that only asks "can I build here".
func doctorChecks(ctx context.Context, res *DoctorResult) error {
	if _, err := exec.LookPath("go"); err != nil {
		hint := fmt.Sprintf("go not found in PATH - install Go >= %d.%d from https://go.dev/dl/", minGoMajor, minGoMinor)
		res.add(DoctorCheck{Name: "go", Status: "error", Hint: hint})
		return exitErrorf(ExitMissing, "missing_prerequisite", "%s", hint)
	}
	res.add(DoctorCheck{Name: "go", Status: "ok"})
	checkGoVersion(ctx, res)

	type tool struct {
		bin  string
		note string
	}
	soft := []tool{
		{"node", "Next.js shell runtime - install Node.js >= 18 LTS (https://nodejs.org)"},
		{"pnpm", "frontend build (scorix dev/build/package) - `npm i -g pnpm` or `corepack enable`"},
	}
	switch runtime.GOOS {
	case "windows":
		soft = append(soft,
			tool{"wix", "Windows MSI packaging (scorix package) - `dotnet tool install --global wix`"},
			tool{"makensis", "NSIS installer (scorix package --format nsis) - `winget install NSIS.NSIS`"},
			tool{"signtool", "Windows code signing (optional; package.sign.windows) - ships with the Windows SDK"},
		)
	case "linux":
		soft = append(soft,
			tool{"linuxdeploy", "Linux AppImage packaging (scorix package)"},
			tool{"gpg", "Linux AppImage signing (optional; package.sign.linux)"},
		)
	case "darwin":
		soft = append(soft,
			tool{"lipo", "macOS universal binaries (scorix package --arch universal)"},
			tool{"hdiutil", "macOS .dmg packaging (scorix package)"},
			tool{"codesign", "macOS code signing (optional; package.sign.macos)"},
			tool{"xcrun", "macOS notarization (optional; notarytool/stapler)"},
		)
	}

	for _, t := range soft {
		if _, err := exec.LookPath(t.bin); err != nil {
			res.add(DoctorCheck{
				Name:   t.bin,
				Status: "warn",
				Hint:   fmt.Sprintf("%s not found - needed for %s", t.bin, t.note),
			})
			continue
		}
		res.add(DoctorCheck{Name: t.bin, Status: "ok"})
	}

	if runtime.GOOS == "windows" {
		checkWebView2Runtime(res)
	}
	checkCrashReports(res)
	return nil
}

// checkCrashReports points at the app's own crashes. Doctor is where someone
// already goes when something is wrong, and the reports otherwise sit in a
// per-OS data directory nobody remembers.
func checkCrashReports(res *DoctorResult) {
	meta, err := loadAppMetadata(".")
	if err != nil || meta.App.Name == "" {
		return // not inside a project, or it has no name: nothing to look up
	}
	dir := diag.Dir(module.DataDir(meta.App.Name))
	reports, err := diag.List(dir)
	if err != nil || len(reports) == 0 {
		return
	}
	res.add(DoctorCheck{
		Name:   "crash reports",
		Status: "warn",
		Detail: fmt.Sprintf("%d", len(reports)),
		Hint:   fmt.Sprintf("%d crash report(s) in %s - newest: %s", len(reports), dir, filepath.Base(reports[0])),
	})
}

func checkGoVersion(ctx context.Context, res *DoctorResult) {
	name := fmt.Sprintf("go >= %d.%d", minGoMajor, minGoMinor)
	out, err := exec.CommandContext(ctx, "go", "version").Output()
	if err != nil {
		res.add(DoctorCheck{Name: name, Status: "warn",
			Hint: fmt.Sprintf("could not run `go version` to verify Go >= %d.%d: %v", minGoMajor, minGoMinor, err)})
		return
	}
	major, minor, ok := parseGoVersion(string(out))
	if !ok {
		res.add(DoctorCheck{Name: name, Status: "warn",
			Hint: fmt.Sprintf("could not parse Go version from %q - project needs Go >= %d.%d",
				strings.TrimSpace(string(out)), minGoMajor, minGoMinor)})
		return
	}
	found := fmt.Sprintf("%d.%d", major, minor)
	if major < minGoMajor || (major == minGoMajor && minor < minGoMinor) {
		res.add(DoctorCheck{Name: name, Status: "warn", Detail: found,
			Hint: fmt.Sprintf("Go %s detected - project needs Go >= %d.%d; upgrade from https://go.dev/dl/",
				found, minGoMajor, minGoMinor)})
		return
	}
	res.add(DoctorCheck{Name: "go version", Status: "ok", Detail: ">= " + fmt.Sprintf("%d.%d", minGoMajor, minGoMinor)})
}

func parseGoVersion(s string) (major, minor int, ok bool) {
	for _, field := range strings.Fields(s) {
		if !strings.HasPrefix(field, "go") {
			continue
		}
		ver := strings.TrimPrefix(field, "go")
		if ver == "" || (ver[0] < '0' || ver[0] > '9') {
			continue
		}
		parts := strings.SplitN(ver, ".", 3)
		if len(parts) < 2 {
			continue
		}
		ma, err1 := strconv.Atoi(parts[0])
		mi, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil {
			continue
		}
		return ma, mi, true
	}
	return 0, 0, false
}

// 107 is what `vite build` targets (Baseline "widely available"); under it a
// bundle can fail to parse and the window opens blank with nothing logged. Raise
// it only with the scaffold's browser target, and update ARCHITECTURE.md §2.3.
const minWebView2Major = 107

func checkWebView2Runtime(res *DoctorResult) {
	name := fmt.Sprintf("WebView2 Runtime >= Chromium %d", minWebView2Major)
	candidates := []string{}
	if pf := os.Getenv("ProgramFiles(x86)"); pf != "" {
		candidates = append(candidates, filepath.Join(pf, "Microsoft", "EdgeWebView", "Application"))
	}
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		candidates = append(candidates, filepath.Join(pf, "Microsoft", "EdgeWebView", "Application"))
	}
	if la := os.Getenv("LocalAppData"); la != "" {
		candidates = append(candidates, filepath.Join(la, "Microsoft", "EdgeWebView", "Application"))
	}

	// Newest across install roots: per-user and machine-wide can coexist, and
	// WebView2 loads the newest.
	bestMajor, bestVer := 0, ""
	for _, dir := range candidates {
		for _, sub := range versionedSubdirs(dir) {
			if m, ok := parseChromiumMajor(sub); ok && m > bestMajor {
				bestMajor, bestVer = m, sub
			}
		}
	}

	if bestVer == "" {
		res.add(DoctorCheck{Name: name, Status: "warn",
			Hint: "WebView2 Runtime not detected - the native window needs the Evergreen WebView2 Runtime " +
				"(https://developer.microsoft.com/microsoft-edge/webview2/). Bundled with Windows 11; install it on older systems."})
		return
	}
	if bestMajor < minWebView2Major {
		res.add(DoctorCheck{Name: name, Status: "warn", Detail: bestVer,
			Hint: fmt.Sprintf("WebView2 Runtime %s is older than Chromium %d, which is what `vite build` targets - "+
				"the shell can open blank with nothing logged. The runtime is evergreen, so a version this old usually means "+
				"a managed fleet is pinning it", bestVer, minWebView2Major)})
		return
	}
	res.add(DoctorCheck{Name: "WebView2 Runtime", Status: "ok", Detail: bestVer})
}

// Version dirs ("120.0.2210.91") sit beside others ("Installer"); the caller
// filters by what parses.
func versionedSubdirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// Refuses a name whose digits do not end at a dot: "1x2.0" must not pass as 1.
func parseChromiumMajor(name string) (int, bool) {
	i := 0
	for i < len(name) && name[i] >= '0' && name[i] <= '9' {
		i++
	}
	if i == 0 || (i < len(name) && name[i] != '.') {
		return 0, false
	}
	n, err := strconv.Atoi(name[:i])
	if err != nil {
		return 0, false
	}
	return n, true
}
