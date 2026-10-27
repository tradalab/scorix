package runner

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/internal/desktopentry"
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
		name     string
		lineNo   int
		execLn   int
	)
	// First occurrence wins: later Exec= lines belong to action groups, not the main entry.
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case execLine == "" && strings.HasPrefix(line, "Exec="):
			execLine, execLn = strings.TrimPrefix(line, "Exec="), lineNo
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
	if name != "" && name != productName {
		// The label may legitimately differ ("S3 Hub" for S3Hub), so note it, don't refuse.
		fmt.Printf("==> note: %s declares Name=%q while the app is %q; that is the label the menu shows\n", path, name, productName)
	}
	return nil
}

// A Wayland dock finds the icon only through a .desktop named after the window's app id.
func desktopID(bc *BuildContext) string {
	if bc.Identifier == "" {
		fmt.Println("==> warning: app.identifier is empty, so the window's app id is the binary's name and a dock may show no icon for it")
	}
	return orDefault(bc.Identifier, bc.ProductName)
}

func linuxDesktopSource(bc *BuildContext) ([]byte, error) {
	desktop := firstExisting(
		filepath.Join(bc.Root, "installer", "linux", bc.ProductName+".desktop"),
		filepath.Join(bc.Root, "installer", "linux", "app.desktop"),
	)
	if desktop == "" {
		fmt.Println("==> Desktop entry not found - scaffolding installer/linux/")
		if err := scaffoldLinuxInstaller(bc); err != nil {
			return nil, fmt.Errorf("scaffold installer: %w", err)
		}
		desktop = filepath.Join(bc.Root, "installer", "linux", "app.desktop")
	}
	if err := checkDesktopIdentity(desktop, bc.ProductName); err != nil {
		return nil, err
	}
	return os.ReadFile(desktop)
}

func linuxIcon(bc *BuildContext) (string, error) {
	icon := firstExisting(
		filepath.Join(bc.Root, "installer", "linux", bc.ProductName+".png"),
		filepath.Join(bc.Root, "installer", "linux", "app.png"),
	)
	if icon == "" && bc.IconPath != "" && strings.EqualFold(filepath.Ext(bc.IconPath), ".png") {
		icon = bc.IconPath
	}
	if icon == "" {
		return "", fmt.Errorf("linux icon not found - provide installer/linux/%s.png (PNG; .ico is not accepted on Linux)", bc.ProductName)
	}
	return icon, nil
}

// On Linux only a .desktop can claim app.protocols.
func linuxDesktopEntry(bc *BuildContext, id string, src []byte, cmd string) []byte {
	keys := map[string]string{"Icon": id, "StartupWMClass": id}
	exec := desktopentry.Get(src, "Exec")
	if exec == "" {
		exec = bc.ProductName
		keys["Exec"] = exec
	}
	if len(bc.Protocols) > 0 {
		if !strings.Contains(exec, "%u") && !strings.Contains(exec, "%U") {
			keys["Exec"] = exec + " %u"
		}
		mime := desktopentry.Get(src, "MimeType")
		if mime != "" && !strings.HasSuffix(mime, ";") {
			mime += ";"
		}
		for _, s := range bc.Protocols {
			if t := "x-scheme-handler/" + strings.ToLower(s) + ";"; !strings.Contains(mime, t) {
				mime += t
			}
		}
		keys["MimeType"] = mime
	}
	return desktopentry.Rewrite(src, cmd, keys)
}
