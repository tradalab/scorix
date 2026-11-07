package browser

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/module"
)

type opened struct{ revealed, launched []string }

type nullCore struct{}

func (nullCore) Register(string, module.Permission, func(context.Context, json.RawMessage) (any, error)) {
}
func (nullCore) Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return nil, errors.New("not used")
}
func (nullCore) Emit(context.Context, string, json.RawMessage) error { return nil }

const testApp = "scorix-browser-test"

func load(t *testing.T, cfg map[string]any) (*BrowserModule, error) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("APPDATA", filepath.Join(home, "data"))
	section := map[string]any{"enabled": true}
	for k, v := range cfg {
		section[k] = v
	}
	m := New()
	cfg2 := &config.Config{Modules: map[string]any{"browser": section}}
	cfg2.App.Name = testApp
	mgr := module.NewManager(cfg2, nullCore{}, nil)
	mgr.Register(m)
	return m, mgr.LoadAll()
}

func scoped(t *testing.T, cfg map[string]any) (*BrowserModule, *opened) {
	t.Helper()
	m, err := load(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := &opened{}
	m.reveal = func(p string) error { got.revealed = append(got.revealed, p); return nil }
	m.open = func(p string) error { got.launched = append(got.launched, p); return nil }
	return m, got
}

func touch(t *testing.T, path string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNoPathsDeclaredMeansNoPathIsTouched(t *testing.T) {
	file := touch(t, filepath.Join(t.TempDir(), "report.csv"), 0o600)
	m, got := scoped(t, nil)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: file}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("RevealPath with no modules.browser.paths: err = %v, want denied", err)
	}
	if _, err := m.OpenPath(context.Background(), PathRequest{Path: file}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("OpenPath with no modules.browser.paths: err = %v, want denied", err)
	}
	if len(got.revealed)+len(got.launched) != 0 {
		t.Fatalf("an unscoped module reached the OS: %+v", got)
	}
}

func TestPathsOutsideTheDeclaredRootsAreDenied(t *testing.T) {
	inside := t.TempDir()
	outside := t.TempDir()
	m, got := scoped(t, map[string]any{"paths": []any{inside}})
	ok := touch(t, filepath.Join(inside, "sub", "a.csv"), 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: ok}); err != nil {
		t.Fatalf("a file under a declared root was refused: %v", err)
	}
	far := touch(t, filepath.Join(outside, "b.csv"), 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: far}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("a file outside every root: err = %v, want denied", err)
	}
	sibling := touch(t, inside+"-evil/c.csv", 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: sibling}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("a sibling sharing the root's prefix: err = %v, want denied", err)
	}
	if len(got.revealed) != 1 {
		t.Fatalf("revealed %v, want only the in-scope file", got.revealed)
	}
}

func TestASymlinkCannotLeadOutOfScope(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege the runner may not have")
	}
	inside := t.TempDir()
	secret := touch(t, filepath.Join(t.TempDir(), "secret.txt"), 0o600)
	link := filepath.Join(inside, "innocent.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	m, _ := scoped(t, map[string]any{"paths": []any{inside}})
	if _, err := m.OpenPath(context.Background(), PathRequest{Path: link}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("a link inside the root pointing outside it: err = %v, want denied", err)
	}
}

func TestRootsExpandDataAndHome(t *testing.T) {
	m, _ := scoped(t, map[string]any{"paths": []any{"$DATA/exports"}})
	data := module.DataDir(testApp)
	file := touch(t, filepath.Join(data, "exports", "x.csv"), 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: file}); err != nil {
		t.Errorf("$DATA/exports did not cover a file under the data dir: %v", err)
	}
	other := touch(t, filepath.Join(data, "db.sqlite"), 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: other}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("$DATA/exports also let through the rest of the data dir: %v", err)
	}
}

func TestAStarDeclaresEveryPath(t *testing.T) {
	file := touch(t, filepath.Join(t.TempDir(), "a.csv"), 0o600)
	m, _ := scoped(t, map[string]any{"paths": []any{"*"}})
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: file}); err != nil {
		t.Errorf(`paths: ["*"] refused a path: %v`, err)
	}
}

func TestARelativeRootIsRefusedAtLoad(t *testing.T) {
	if _, err := load(t, map[string]any{"paths": []any{"downloads"}}); err == nil {
		t.Fatal("a relative root loaded: it would mean whatever the working directory happens to be")
	}
}

func TestOpenPathNeverLaunchesAProgram(t *testing.T) {
	dir := t.TempDir()
	m, got := scoped(t, map[string]any{"paths": []any{"*"}})
	for _, name := range []string{"setup.exe", "run.BAT", "x.ps1", "y.lnk", "z.desktop", "w.command", "v.jar", "u.msi", "help.chm", "c.msc", "e.scf", "f.application", "g.appref-ms", "h.settingcontent-ms", "i.inf", "j.xll", "disk.iso", "disk.img", "disk.vhdx", "k.mpkg"} {
		p := touch(t, filepath.Join(dir, name), 0o600)
		if _, err := m.OpenPath(context.Background(), PathRequest{Path: p}); fault.CodeOf(err) != fault.CodeDenied {
			t.Errorf("OpenPath(%s): err = %v, want denied", name, err)
		}
	}
	if runtime.GOOS != "windows" {
		p := touch(t, filepath.Join(dir, "tool"), 0o700)
		if _, err := m.OpenPath(context.Background(), PathRequest{Path: p}); fault.CodeOf(err) != fault.CodeDenied {
			t.Errorf("OpenPath on a file with the execute bit: err = %v, want denied", err)
		}
	}
	bundle := filepath.Join(dir, "Evil.app")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenPath(context.Background(), PathRequest{Path: bundle}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("OpenPath on an .app bundle: err = %v, want denied", err)
	}
	doc := touch(t, filepath.Join(dir, "report.pdf"), 0o600)
	if _, err := m.OpenPath(context.Background(), PathRequest{Path: doc}); err != nil {
		t.Errorf("OpenPath refused a document: %v", err)
	}
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: filepath.Join(dir, "setup.exe")}); err != nil {
		t.Errorf("RevealPath refused an executable, but showing it in a folder launches nothing: %v", err)
	}
	if len(got.launched) != 1 || got.launched[0] != doc {
		t.Fatalf("launched %v, want only the document", got.launched)
	}
}

func TestOpenPathJudgesWhatALinkPointsAt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege the runner may not have")
	}
	dir := t.TempDir()
	m, got := scoped(t, map[string]any{"paths": []any{"*"}})
	prog := touch(t, filepath.Join(dir, "setup.exe"), 0o600)
	link := filepath.Join(dir, "report.pdf")
	if err := os.Symlink(prog, link); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenPath(context.Background(), PathRequest{Path: link}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("report.pdf -> setup.exe: err = %v, want denied", err)
	}
	bundle := filepath.Join(dir, "Evil.app")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(dir, "Documents")
	if err := os.Symlink(bundle, folder); err != nil {
		t.Fatal(err)
	}
	if _, err := m.OpenPath(context.Background(), PathRequest{Path: folder}); fault.CodeOf(err) != fault.CodeDenied {
		t.Errorf("Documents -> Evil.app: err = %v, want denied", err)
	}
	if len(got.launched) != 0 {
		t.Fatalf("launched %v through a link", got.launched)
	}
}

func TestOpenPathSeesTheNameWindowsWillRun(t *testing.T) {
	dir := t.TempDir()
	m, got := scoped(t, map[string]any{"paths": []any{"*"}})
	for _, name := range []string{"setup.exe.", "setup.exe. ."} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Skipf("this filesystem refuses %q: %v", name, err)
		}
		if _, err := m.OpenPath(context.Background(), PathRequest{Path: p}); fault.CodeOf(err) != fault.CodeDenied {
			t.Errorf("OpenPath(%q): err = %v, want denied: Windows drops the trailing dots and spaces and runs setup.exe", name, err)
		}
	}
	if len(got.launched) != 0 {
		t.Fatalf("launched %v", got.launched)
	}
}

func TestARootCreatedAfterLoadStillMatchesThroughASymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs a privilege the runner may not have")
	}
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	m, _ := scoped(t, map[string]any{"paths": []any{filepath.Join(alias, "exports")}})
	file := touch(t, filepath.Join(alias, "exports", "x.csv"), 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: file}); err != nil {
		t.Errorf("a root that did not exist at load, under a symlinked parent, refused its own file: %v", err)
	}
}

func TestRootsTakeTheHostSeparatorAfterAVariable(t *testing.T) {
	m, _ := scoped(t, map[string]any{"paths": []any{"$DATA" + string(filepath.Separator) + "exports"}})
	file := touch(t, filepath.Join(module.DataDir(testApp), "exports", "x.csv"), 0o600)
	if _, err := m.RevealPath(context.Background(), PathRequest{Path: file}); err != nil {
		t.Errorf("$DATA%cexports did not load as a root: %v", filepath.Separator, err)
	}
}
