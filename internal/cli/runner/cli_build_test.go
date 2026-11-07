package runner

import (
	"slices"
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

func TestDesktopBuildIsStrippedWithoutAppLdflags(t *testing.T) {
	for _, goos := range []string{"windows", "darwin", "linux"} {
		f := strings.Fields(buildLdflags(&BuildContext{OS: goos}))
		if !slices.Contains(f, "-s") || !slices.Contains(f, "-w") {
			t.Errorf("%s: ldflags %q keep the symbol table and DWARF", goos, f)
		}
	}
	f := strings.Fields(buildLdflags(&BuildContext{OS: "linux", Ldflags: []string{"-s", "-w", "-X main.v=1"}}))
	if n := len(slices.DeleteFunc(slices.Clone(f), func(s string) bool { return s != "-s" })); n != 1 {
		t.Errorf("an app that already strips gets -s %d times: %q", n, f)
	}
	if !slices.Contains(f, "main.v=1") {
		t.Errorf("app ldflags dropped: %q", f)
	}
}
