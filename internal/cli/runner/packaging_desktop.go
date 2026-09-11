package runner

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// A .desktop is stamped from a sibling app by hand, and nothing else compares it to scorix.yaml.
func checkDesktopIdentity(path, productName string) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()

	var (
		execLine string
		icon     string
		name     string
		lineNo   int
		execLn   int
		iconLn   int
	)
	// First occurrence wins: later Exec= lines belong to action groups, not the main entry.
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case execLine == "" && strings.HasPrefix(line, "Exec="):
			execLine, execLn = strings.TrimPrefix(line, "Exec="), lineNo
		case icon == "" && strings.HasPrefix(line, "Icon="):
			icon, iconLn = strings.TrimPrefix(line, "Icon="), lineNo
		case name == "" && strings.HasPrefix(line, "Name="):
			name = strings.TrimPrefix(line, "Name=")
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	// Exec carries field codes (`%u`, `%F`); the command is the first word.
	command := execLine
	if i := strings.IndexByte(command, ' '); i >= 0 {
		command = command[:i]
	}
	if command != "" && command != productName {
		return fmt.Errorf(
			"%s:%d: Exec is %q but this app is %q.\n"+
				"The AppImage contains only %s, so a launcher pointing at another name does nothing when clicked. "+
				"Set Exec=%s (field codes after it are fine)",
			path, execLn, command, productName, productName, productName)
	}
	if icon != "" && icon != productName {
		return fmt.Errorf(
			"%s:%d: Icon is %q but this app is %q.\n"+
				"The icon is looked up by name inside the AppDir, where the only entry is %s.png, so a stale name falls back to a generic placeholder with nothing logged. Set Icon=%s",
			path, iconLn, icon, productName, productName, productName)
	}
	if name != "" && name != productName {
		// The label may legitimately differ ("S3 Hub" for S3Hub), so note it, don't refuse.
		fmt.Printf("==> note: %s declares Name=%q while the app is %q; that is the label the menu shows\n", path, name, productName)
	}
	return nil
}
