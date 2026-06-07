package runner

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
)

func Doctor(ctx context.Context) error {
	fmt.Println("Checking toolchain...")

	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go not found in PATH")
	}
	fmt.Println("OK: go")

	type tool struct {
		bin  string
		note string
	}
	soft := []tool{
		{"pnpm", "frontend build (scorix dev/build/package)"},
		{"gcc", "CGO (sqlite/webview) — required to build the desktop app"},
	}
	switch runtime.GOOS {
	case "windows":
		soft = append(soft,
			tool{"wix", "Windows MSI packaging (scorix package)"},
			tool{"signtool", "Windows code signing (optional; package.sign.windows)"},
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
	return nil
}
