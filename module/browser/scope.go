package browser

import (
	"fmt"
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

type pathScope struct {
	any   bool
	roots []string
}

func resolveScope(paths []string, dataDir string) (pathScope, error) {
	var s pathScope
	for _, raw := range paths {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if raw == "*" {
			s.any = true
			continue
		}
		root, err := expandRoot(raw, dataDir)
		if err != nil {
			return pathScope{}, err
		}
		s.roots = append(s.roots, root)
	}
	return s, nil
}

func expandRoot(raw, dataDir string) (string, error) {
	vars := map[string]func() (string, error){
		"$HOME":  os.UserHomeDir,
		"$CACHE": os.UserCacheDir,
		"$TEMP":  func() (string, error) { return os.TempDir(), nil },
		"$DATA": func() (string, error) {
			if dataDir == "" {
				return "", fmt.Errorf("the app has no data dir")
			}
			return dataDir, nil
		},
	}
	for name, resolve := range vars {
		if raw != name && !strings.HasPrefix(raw, name+"/") && !strings.HasPrefix(raw, name+string(filepath.Separator)) {
			continue
		}
		base, err := resolve()
		if err != nil {
			return "", fmt.Errorf("modules.browser.paths: %s: %w", name, err)
		}
		return filepath.Join(base, filepath.FromSlash(strings.TrimPrefix(raw, name))), nil
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("modules.browser.paths: %q must be absolute or start with $HOME, $DATA, $CACHE or $TEMP", raw)
	}
	return filepath.Clean(raw), nil
}

func canonical(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		p = strings.ToLower(p)
	}
	return p
}

func (s pathScope) admits(path string) error {
	if s.any {
		return nil
	}
	target := canonical(path)
	for _, root := range s.roots {
		rel, err := filepath.Rel(canonical(root), target)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return nil
		}
	}
	return fault.Errorf(fault.CodeDenied, "path %q is outside modules.browser.paths", path).With("path", path)
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
