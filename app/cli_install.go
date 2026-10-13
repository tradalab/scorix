package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	ipc "github.com/tradalab/scorix/internal/ipc"
)

type CLIInstall struct {
	Name      string `json:"name"`      // what the user types
	Link      string `json:"link"`      // where the command lives on PATH
	Target    string `json:"target"`    // the binary it runs
	Installed bool   `json:"installed"` // Link resolves to Target
	OnPath    bool   `json:"on_path"`   // Link's directory is on PATH, so typing Name works
	// Someone else's command (a .deb's /usr/bin/<name>, a Homebrew link, the
	// MSI's): never ours to add or remove.
	Packaged bool   `json:"packaged,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

func (a *App) cliName() string {
	if a.cfg.CLI.Name != "" {
		return a.cfg.CLI.Name
	}
	return strings.ToLower(strings.ReplaceAll(a.cfg.App.Name, " ", "-"))
}

// Not /usr/local/bin: that needs admin rights; this is on PATH on most Linux desktops.
func cliLinkDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "bin"), nil
}

func (a *App) cliInstallStatus() (CLIInstall, error) {
	st := CLIInstall{Name: a.cliName()}
	target, err := mcpSelfCommand()
	if err != nil {
		return st, err
	}
	st.Target = target
	if runtime.GOOS == "windows" {
		// The MSI puts <App>.com on PATH: the command is the product name,
		// whatever cli.name says.
		st.Link = strings.TrimSuffix(target, filepath.Ext(target)) + ".com"
		st.Name = strings.TrimSuffix(filepath.Base(target), filepath.Ext(target))
		st.Packaged = true
		if found, err := exec.LookPath(st.Name); err == nil && strings.EqualFold(filepath.Clean(found), filepath.Clean(st.Link)) {
			st.Installed, st.OnPath = true, true
		} else {
			st.Hint = "reinstall " + a.cfg.App.Name + " to put " + st.Name + " on PATH, then open a new terminal"
		}
		return st, nil
	}
	dir, err := cliLinkDir()
	if err != nil {
		return st, err
	}
	st.Link = filepath.Join(dir, st.Name)
	if found, err := exec.LookPath(st.Name); err == nil && filepath.Clean(found) != filepath.Clean(st.Link) && sameFile(found, target) {
		st.Link, st.Installed, st.OnPath, st.Packaged = found, true, true, true
		return st, nil
	}
	if dest, err := os.Readlink(st.Link); err == nil && filepath.Clean(dest) == filepath.Clean(target) {
		st.Installed = true
	}
	st.OnPath = onPath(dir)
	if st.Installed && !st.OnPath {
		st.Hint = fmt.Sprintf("add %s to PATH: export PATH=\"%s:$PATH\" in your shell's rc file", dir, dir)
	}
	return st, nil
}

func sameFile(a, b string) bool {
	sa, errA := os.Stat(a)
	sb, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(sa, sb)
}

func onPath(dir string) bool {
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(p) == filepath.Clean(dir) {
			return true
		}
	}
	return false
}

func (a *App) cliInstallLink() (CLIInstall, error) {
	st, err := a.cliInstallStatus()
	if err != nil || runtime.GOOS == "windows" || st.Installed {
		return st, err
	}
	if fi, err := os.Lstat(st.Link); err == nil {
		if fi.Mode()&os.ModeSymlink == 0 {
			return st, fmt.Errorf("%s exists and is not a link this app made; remove it yourself if it is safe to", st.Link)
		}
		if !a.replaceableLink(st.Link) {
			dest, _ := os.Readlink(st.Link)
			return st, fmt.Errorf("%s already points at %s, another program; remove it yourself if it is safe to", st.Link, dest)
		}
		if err := os.Remove(st.Link); err != nil {
			return st, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(st.Link), 0o755); err != nil {
		return st, err
	}
	if err := os.Symlink(st.Target, st.Link); err != nil {
		return st, err
	}
	return a.cliInstallStatus()
}

// A dangling link, or one to another copy of this app, is ours to move; one to
// another program, say a `code` linked by hand, is not.
func (a *App) replaceableLink(link string) bool {
	if _, err := os.Stat(link); err != nil {
		return true
	}
	dest, err := os.Readlink(link)
	if err != nil {
		return false
	}
	return sameAppName(filepath.Base(dest), a.cfg.App.Name)
}

// "My App", "My_App-1.2.AppImage" and "myapp" are one app; "LoomPro" is not Loom.
func sameAppName(base, app string) bool {
	norm := func(s string) string {
		return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(s))
	}
	b, n := norm(base), norm(app)
	if n == "" || !strings.HasPrefix(b, n) {
		return false
	}
	rest := b[len(n):]
	return rest == "" || rest[0] == '.' || (rest[0] >= '0' && rest[0] <= '9')
}

// A link to another install of the same app belongs to that install.
func (a *App) cliUninstallLink() (CLIInstall, error) {
	st, err := a.cliInstallStatus()
	if err != nil || runtime.GOOS == "windows" {
		return st, err
	}
	if st.Packaged {
		return st, fmt.Errorf("%s belongs to the installed package; remove the package to remove the command", st.Link)
	}
	if !st.Installed {
		if _, err := os.Lstat(st.Link); err == nil {
			return st, fmt.Errorf("%s points somewhere else; leaving it", st.Link)
		}
		return st, nil
	}
	if err := os.Remove(st.Link); err != nil {
		return st, err
	}
	return a.cliInstallStatus()
}

var errCLIInstallWindows = errors.New("on Windows the installer puts the command on PATH; there is nothing to install by hand")

func (a *App) runCLIInstall(verb string, io cliIO, asJSON bool) int {
	var st CLIInstall
	var err error
	switch verb {
	case "", "status":
		st, err = a.cliInstallStatus()
	case "install":
		if runtime.GOOS == "windows" {
			err = errCLIInstallWindows
			break
		}
		st, err = a.cliInstallLink()
	case "uninstall":
		if runtime.GOOS == "windows" {
			err = errCLIInstallWindows
			break
		}
		st, err = a.cliUninstallLink()
	default:
		fmt.Fprintln(io.err, "error: usage: cli install | uninstall | status")
		return ExitUsage
	}
	if err != nil {
		fmt.Fprintln(io.err, "error:", err)
		return ExitError
	}
	if asJSON {
		b, _ := json.Marshal(st)
		fmt.Fprintln(io.out, string(b))
		return ExitOK
	}
	b, _ := json.Marshal(st)
	renderHuman(io.out, b)
	return ExitOK
}

// For a settings button: without the command on PATH, nobody can type it.
func (a *App) registerCLIInstallCommands() {
	reply := func(fn func() (CLIInstall, error)) ipc.CmdFunc {
		return func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
			if !a.cfg.CLI.Enabled {
				return nil, errors.New("this build has no command line")
			}
			return fn()
		}
	}
	a.reg.Command("sys:cli:status", reply(a.cliInstallStatus))
	a.reg.Command("sys:cli:install", reply(a.cliInstallLink))
	a.reg.Command("sys:cli:uninstall", reply(a.cliUninstallLink))
}
