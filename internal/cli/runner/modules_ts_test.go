package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestGenerateWritesTheModuleClients(t *testing.T) {
	dir := newNativeProject(t, nativeProto)
	yaml := "name: example.com/demo\napp:\n  name: demo\n  identifier: com.example.demo\nmodules:\n  share:\n    enabled: true\n  clipboard:\n    enabled: false\n"
	// sqlx is only imported, as model code does.
	svc := "package svc\n\nimport (\n\tb \"github.com/tradalab/scorix/module/browser\"\n\t\"github.com/tradalab/scorix/module/sqlx\"\n)\n\n" +
		"func Register(a interface{ Module(any) }) { a.Module(b.New()) }\n\nvar _ = sqlx.New\n"
	for rel, body := range map[string]string{"scorix.yaml": yaml, "internal/svc/register.go": svc} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	ts := readGenerated(t, dir, "shell/api/modules.ts")
	for _, want := range []string{`import scorix, { isWebMode, webModeReady } from "@/lib/scorix";`, "export const share = {", "export const browser = {", `"mod:browser:OpenUrl"`, "export function openExternal("} {
		if !strings.Contains(ts, want) {
			t.Errorf("modules.ts lacks %s:\n%s", want, ts)
		}
	}
	for _, not := range []string{"export const clipboard", "export const sqlx", "export function writeClipboard("} {
		if strings.Contains(ts, not) {
			t.Errorf("modules.ts has %s, which is off or only imported:\n%s", not, ts)
		}
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("--check right after generate: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "shell", "api", "modules.ts")); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("--check on an app that never generated modules.ts: %v", err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "scorix.yaml"), []byte("name: example.com/demo\napp:\n  name: demo\n  identifier: com.example.demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "internal", "svc", "register.go")); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if ts := readGenerated(t, dir, "shell/api/modules.ts"); strings.Contains(ts, "export const") || strings.Contains(ts, "import scorix") {
		t.Errorf("modules.ts still has clients:\n%s", ts)
	}
}

func TestRegisteredModulesFollowsAVariable(t *testing.T) {
	dir := t.TempDir()
	src := "package svc\n\nimport (\n\tscorixsqlx \"github.com/tradalab/scorix/module/sqlx\"\n\t\"github.com/tradalab/scorix/module/store\"\n)\n\n" +
		"func Register(a interface{ Module(any) }) {\n\tsqlxMod := scorixsqlx.New()\n\ta.Module(sqlxMod)\n\ta.Module(&store.StoreModule{})\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "register.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := registeredModules(dir)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"sqlx", "store"}) {
		t.Errorf("got %v", got)
	}
}

func TestGenerateWithoutAProtoWritesOnlyTheModuleClients(t *testing.T) {
	main := "package main\n\nimport \"github.com/tradalab/scorix/module/share\"\n\nfunc main() { a.Module(share.New()) }\n"
	dir := project(t, "name: example.com/demo\n", "", "")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(dir, "shell", "api", "modules.ts")) {
		t.Fatal("modules.ts written for a shell without lib/scorix to import")
	}
	if err := os.MkdirAll(filepath.Join(dir, "shell", "lib", "scorix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if ts := readGenerated(t, dir, "shell/api/modules.ts"); !strings.Contains(ts, "export const share = {") || !strings.Contains(ts, `import scorix from "@/lib/scorix";`) {
		t.Errorf("modules.ts lacks the share client or imports isWebMode it never calls:\n%s", ts)
	}
	if got := readGenerated(t, dir, "main.go"); got != main {
		t.Errorf("main.go was rewritten:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "internal")); !os.IsNotExist(err) {
		t.Errorf("internal/ was generated: %v", err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("--check right after generate: %v", err)
	}
}

func TestRegisteredModulesSkipsOnlyDoNotEditFiles(t *testing.T) {
	dir := t.TempDir()
	for name, header := range map[string]string{"modules.go": "// Code generated by scorix. DO NOT EDIT.", "main.go": "// Code generated by scorix."} {
		pkg := map[string]string{"modules.go": "clipboard", "main.go": "share"}[name]
		src := header + "\n\npackage main\n\nimport \"github.com/tradalab/scorix/module/" + pkg + "\"\n\nfunc register(a interface{ Module(any) }) { a.Module(" + pkg + ".New()) }\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := registeredModules(dir); err != nil || !slices.Equal(got, []string{"share"}) {
		t.Errorf("got %v, %v", got, err)
	}
}

func TestGenerateWritesTheRuntimeClientAndCheckCatchesAHandEdit(t *testing.T) {
	dir := newNativeProject(t, nativeProto)
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	lib := readGenerated(t, dir, "shell/lib/scorix/index.ts")
	for _, want := range []string{"Code generated by scorix. DO NOT EDIT.", "export function isWebMode(): boolean", "export default scorix;", "export function isScorixError(", "subscribe(topic: string, callback"} {
		if !strings.Contains(lib, want) {
			t.Errorf("lib/scorix/index.ts lacks %s", want)
		}
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err != nil {
		t.Fatalf("--check right after generate: %v", err)
	}
	path := filepath.Join(dir, "shell", "lib", "scorix", "index.ts")
	if err := os.WriteFile(path, []byte(lib+"export const mine = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err == nil {
		t.Fatal("--check passed over a hand edit of lib/scorix/index.ts")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir, Check: true}); err == nil {
		t.Fatal("--check passed with lib/scorix/index.ts missing while api/ imports it")
	}
}

func TestGenerateLeavesAHandWiredClientAloneAndSkipsTheHelpersThatNeedIt(t *testing.T) {
	main := "package main\n\nimport \"github.com/tradalab/scorix/module/browser\"\n\nfunc main() { a.Module(browser.New()) }\n"
	dir := project(t, "name: example.com/demo\n", "", "")
	shim := filepath.Join(dir, "shell", "lib", "scorix.ts")
	for path, body := range map[string]string{filepath.Join(dir, "main.go"): main, shim: "export default {};\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := GenerateProto(context.Background(), GenerateProtoOptions{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(dir, "shell", "lib", "scorix", "index.ts")) {
		t.Error("lib/scorix/index.ts written beside a hand-wired lib/scorix.ts that shadows nothing")
	}
	ts := readGenerated(t, dir, "shell/api/modules.ts")
	if !strings.Contains(ts, "export const browser = {") || strings.Contains(ts, "isWebMode") || strings.Contains(ts, "openExternal") {
		t.Errorf("modules.ts must keep the browser client but not lean on isWebMode the shim lacks:\n%s", ts)
	}
	if got := readGenerated(t, dir, "shell/lib/scorix.ts"); got != "export default {};\n" {
		t.Errorf("the hand-wired shim was rewritten:\n%s", got)
	}
}

func TestShellClientTreatsAnythingButALoneIndexTsAsHandWired(t *testing.T) {
	for _, c := range []struct {
		files []string
		want  runtimeClient
	}{
		{nil, noClient},
		{[]string{"shell/lib/scorix/index.ts"}, generatedClient},
		{[]string{"shell/lib/scorix/index.tsx"}, handWiredClient},
		{[]string{"shell/lib/scorix/index.ts", "shell/lib/scorix/bridge.ts"}, handWiredClient},
		{[]string{"shell/lib/scorix.ts"}, handWiredClient},
	} {
		dir := t.TempDir()
		for _, f := range c.files {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, f), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if got := shellClient(dir); got != c.want {
			t.Errorf("%v: got %d, want %d", c.files, got, c.want)
		}
	}
}
