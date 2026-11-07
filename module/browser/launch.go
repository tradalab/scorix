package browser

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/tradalab/scorix/fault"
)

// Config is modules.browser. Paths lists the roots RevealPath and OpenPath may touch:
// absolute paths, or ones starting with $HOME, $DATA, $CACHE or $TEMP. Empty grants
// nothing; ["*"] grants every path.
type Config struct {
	Paths []string `json:"paths"`
}

var launchers = map[string]bool{
	".exe": true, ".com": true, ".bat": true, ".cmd": true, ".msi": true, ".msp": true,
	".ps1": true, ".psm1": true, ".vbs": true, ".vbe": true, ".js": true, ".jse": true,
	".wsf": true, ".wsh": true, ".hta": true, ".cpl": true, ".scr": true, ".pif": true,
	".lnk": true, ".url": true, ".reg": true, ".jar": true, ".app": true, ".command": true,
	".desktop": true, ".sh": true, ".appimage": true, ".run": true, ".pkg": true,
	".deb": true, ".rpm": true, ".workflow": true, ".terminal": true, ".mpkg": true,
	".chm": true, ".msc": true, ".scf": true, ".application": true, ".appref-ms": true,
	".gadget": true, ".ws": true, ".settingcontent-ms": true, ".inf": true, ".xll": true,
	".iso": true, ".img": true, ".vhd": true, ".vhdx": true,
}

func launchesAsProgram(name string) bool {
	return launchers[strings.ToLower(filepath.Ext(strings.TrimRight(name, ". ")))]
}

func refuseLaunch(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fault.Wrap(fault.CodeNotFound, err)
	}
	real := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		real = r
	}
	if launchesAsProgram(path) || launchesAsProgram(real) || (info.Mode().IsRegular() && info.Mode()&fs.ModePerm&0o111 != 0 && runtime.GOOS != "windows") {
		return fault.Errorf(fault.CodeDenied, "OpenPath does not launch programs: %q", path).With("path", path)
	}
	return nil
}
