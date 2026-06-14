package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	minGoMajor = 1
	minGoMinor = 26
)

func Doctor(ctx context.Context) error {
	fmt.Println("Checking toolchain...")

	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go not found in PATH — install Go >= %d.%d from https://go.dev/dl/", minGoMajor, minGoMinor)
	}
	fmt.Println("OK: go")
	checkGoVersion(ctx)

	type tool struct {
		bin  string
		note string
	}
	soft := []tool{
		{"node", "Next.js shell runtime — install Node.js >= 18 LTS (https://nodejs.org)"},
		{"pnpm", "frontend build (scorix dev/build/package) — `npm i -g pnpm` or `corepack enable`"},
	}
	switch runtime.GOOS {
	case "windows":
		soft = append(soft,
			tool{"wix", "Windows MSI packaging (scorix package) — `dotnet tool install --global wix`"},
			tool{"signtool", "Windows code signing (optional; package.sign.windows) — ships with the Windows SDK"},
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
			fmt.Printf("WARN: %s not found — needed for %s\n", t.bin, t.note)
			continue
		}
		fmt.Printf("OK: %s\n", t.bin)
	}

	if runtime.GOOS == "windows" {
		checkWebView2Runtime()
	}

	return nil
}

func checkGoVersion(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "go", "version").Output()
	if err != nil {
		fmt.Printf("WARN: could not run `go version` to verify Go >= %d.%d: %v\n", minGoMajor, minGoMinor, err)
		return
	}
	major, minor, ok := parseGoVersion(string(out))
	if !ok {
		fmt.Printf("WARN: could not parse Go version from %q — project needs Go >= %d.%d\n",
			strings.TrimSpace(string(out)), minGoMajor, minGoMinor)
		return
	}
	if major < minGoMajor || (major == minGoMajor && minor < minGoMinor) {
		fmt.Printf("WARN: Go %d.%d detected — project needs Go >= %d.%d; upgrade from https://go.dev/dl/\n",
			major, minor, minGoMajor, minGoMinor)
		return
	}
	fmt.Printf("OK: go version >= %d.%d\n", minGoMajor, minGoMinor)
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

func checkWebView2Runtime() {
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
	for _, dir := range candidates {
		if hasVersionedSubdir(dir) {
			fmt.Println("OK: WebView2 Runtime")
			return
		}
	}
	fmt.Println("WARN: WebView2 Runtime not detected — the native window needs the Evergreen WebView2 Runtime " +
		"(https://developer.microsoft.com/microsoft-edge/webview2/). Bundled with Windows 11; install it on older systems.")
}

func hasVersionedSubdir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) > 0 && e.Name()[0] >= '0' && e.Name()[0] <= '9' {
			return true
		}
	}
	return false
}
