//go:build windows

package deeplink

import (
	"fmt"
	"os"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// Writes real HKCU keys under throwaway names and removes them again.
func TestRegisterWindows(t *testing.T) {
	scheme := fmt.Sprintf("scorixtest%d", os.Getpid())
	ext := fmt.Sprintf(".scorixtest%d", os.Getpid())
	progID := "com.scorix.test" + ext
	t.Cleanup(func() {
		for _, k := range []string{
			`Software\Classes\` + scheme + `\shell\open\command`,
			`Software\Classes\` + scheme + `\shell\open`,
			`Software\Classes\` + scheme + `\shell`,
			`Software\Classes\` + scheme,
			`Software\Classes\` + progID + `\shell\open\command`,
			`Software\Classes\` + progID + `\shell\open`,
			`Software\Classes\` + progID + `\shell`,
			`Software\Classes\` + progID,
			`Software\Classes\` + ext,
		} {
			_ = registry.DeleteKey(registry.CURRENT_USER, k)
		}
	})

	exe := `C:\apps\demo.exe`
	if err := Register("com.scorix.test", "Scorix Test", exe, []string{scheme}, []string{ext}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\`+scheme, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("scheme key missing: %v", err)
	}
	defer k.Close()
	if _, _, err := k.GetStringValue("URL Protocol"); err != nil {
		t.Fatalf("URL Protocol marker missing: %v", err)
	}

	cmd, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\`+scheme+`\shell\open\command`, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("scheme command missing: %v", err)
	}
	defer cmd.Close()
	if v, _, _ := cmd.GetStringValue(""); v != `"`+exe+`" "%1"` {
		t.Fatalf("scheme command = %q", v)
	}

	extKey, err := registry.OpenKey(registry.CURRENT_USER, `Software\Classes\`+ext, registry.QUERY_VALUE)
	if err != nil {
		t.Fatalf("ext key missing: %v", err)
	}
	defer extKey.Close()
	if v, _, _ := extKey.GetStringValue(""); v != progID {
		t.Fatalf("ext progid = %q, want %q", v, progID)
	}
}
