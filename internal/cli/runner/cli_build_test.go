package runner

import (
	"strings"
	"testing"
)

func TestConsoleBuildDropsWindowsGUI(t *testing.T) {
	gui := &BuildContext{OS: "windows"}
	if !strings.Contains(buildLdflags(gui), "-H=windowsgui") {
		t.Fatal("the exe lost -H=windowsgui")
	}
	con := *gui
	con.console = true
	if strings.Contains(buildLdflags(&con), "windowsgui") {
		t.Fatal("the console build is linked for the GUI")
	}
	if got := consolePath(`C:\b\Loom.exe`); got != `C:\b\Loom.com` {
		t.Errorf("consolePath = %s", got)
	}
}
