//go:build windows

package deeplink

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// Register writes the HKCU protocol and file-type handlers pointing at exe.
// Run on every startup: HKCU needs no elevation, and rewriting keeps the
// command path fresh across updates and dev builds. The MSI can add HKLM
// entries later; HKCU wins per-user either way.
func Register(identifier, appName, exe string, schemes, exts []string) error {
	for _, s := range schemes {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if err := writeProgID(`Software\Classes\`+s, "URL:"+appName, exe, true); err != nil {
			return fmt.Errorf("register scheme %s: %w", s, err)
		}
	}
	for _, e := range exts {
		e = strings.ToLower(strings.TrimSpace(e))
		if e == "" {
			continue
		}
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		progID := identifier + e
		if err := writeProgID(`Software\Classes\`+progID, appName+" file", exe, false); err != nil {
			return fmt.Errorf("register type %s: %w", e, err)
		}
		extKey, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Classes\`+e, registry.SET_VALUE)
		if err != nil {
			return fmt.Errorf("register ext %s: %w", e, err)
		}
		err = extKey.SetStringValue("", progID)
		_ = extKey.Close()
		if err != nil {
			return fmt.Errorf("register ext %s: %w", e, err)
		}
	}
	return nil
}

func writeProgID(path, display, exe string, urlProtocol bool) error {
	key, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	if err := key.SetStringValue("", display); err != nil {
		return err
	}
	if urlProtocol {
		// The empty "URL Protocol" value is what makes the shell treat the
		// class as a scheme handler at all.
		if err := key.SetStringValue("URL Protocol", ""); err != nil {
			return err
		}
	}
	cmd, _, err := registry.CreateKey(registry.CURRENT_USER, path+`\shell\open\command`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer cmd.Close()
	return cmd.SetStringValue("", `"`+exe+`" "%1"`)
}
