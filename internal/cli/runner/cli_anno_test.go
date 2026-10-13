package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/internal/cli/template"
)

const cliProtoHead = `syntax = "proto3";
package testapp;

message Req { string name = 1; repeated string tags = 2; Res nested = 3; bytes blob = 4; }
message Res { string ok = 1; }
message EnvVar { string name = 1; string value = 2; }
message ExecSpec {
  string path = 1;
  repeated string args = 2;
  repeated EnvVar env = 3;
  repeated string unset = 4;
  string dir = 5;
}

service Account {
`

func cliService(t *testing.T, body string) protoFile {
	t.Helper()
	pf, err := parseProto(cliProtoHead + body + "}\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return pf
}

func enrichErr(t *testing.T, body string) error {
	t.Helper()
	pf := cliService(t, body)
	_, _, err := enrichProto(&pf)
	return err
}

func TestCLIAnnotationReadsEveryModifier(t *testing.T) {
	pf := cliService(t, `
  // Starts claude with an account.
  // @cli exec headless destructive timeout=30s arg=name
  rpc Run (Req) returns (ExecSpec);

  // Not on the command line.
  rpc Quiet (Req) returns (Res);
`)
	r := pf.Services[0].RPCs
	if !r[0].CLI || !r[0].CLIExec || !r[0].CLIHeadless || !r[0].CLIDestructive || r[0].CLITimeout != 30*time.Second || r[0].CLIArg != "name" {
		t.Errorf("modifiers lost: %+v", r[0])
	}
	if r[1].CLI {
		t.Error("the marker leaked to the next rpc")
	}
}

func TestCLIRefusesWhatWouldShipWrong(t *testing.T) {
	cases := map[string]string{
		"misspelt modifier":   "  // Doc.\n  // @cli headles\n  rpc A (Req) returns (Res);\n",
		"zero timeout":        "  // Doc.\n  // @cli timeout=0s\n  rpc A (Req) returns (Res);\n",
		"empty arg":           "  // Doc.\n  // @cli arg=\n  rpc A (Req) returns (Res);\n",
		"trailing annotation": "  rpc A (Req) returns (Res); // @cli headless\n",
	}
	for name, body := range cases {
		if _, err := parseProto(cliProtoHead + body + "}\n"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestCLIEnrichRefusals(t *testing.T) {
	cases := map[string]string{
		"no description":      "  // @cli\n  rpc A (Req) returns (Res);\n",
		"on an event":         "  // Doc.\n  // @cli\n  // @event\n  rpc A (Req) returns (Res);\n",
		"arg not a field":     "  // Doc.\n  // @cli arg=nope\n  rpc A (Req) returns (Res);\n",
		"arg repeated":        "  // Doc.\n  // @cli arg=tags\n  rpc A (Req) returns (Res);\n",
		"arg a message":       "  // Doc.\n  // @cli arg=nested\n  rpc A (Req) returns (Res);\n",
		"arg bytes":           "  // Doc.\n  // @cli arg=blob\n  rpc A (Req) returns (Res);\n",
		"exec on a stream":    "  // Doc.\n  // @cli exec\n  rpc A (Req) returns (stream ExecSpec);\n",
		"exec reply not spec": "  // Doc.\n  // @cli exec\n  rpc A (Req) returns (Res);\n",
		"secret repeated":     "  // Doc.\n  // @cli secret=tags\n  rpc A (Req) returns (Res);\n",
		"secret not a string": "  // Doc.\n  // @cli secret=blob\n  rpc A (Req) returns (Res);\n",
		"secret is the arg":   "  // Doc.\n  // @cli arg=name secret=name\n  rpc A (Req) returns (Res);\n",
	}
	for name, body := range cases {
		if err := enrichErr(t, body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestExecSpecShapeIsChecked(t *testing.T) {
	head := strings.Replace(cliProtoHead, "service Account {", "", 1)
	run := "service Account {\n  // Doc.\n  // @cli exec\n  rpc Run (Req) returns (Spec);\n}\n"
	cases := map[string]string{
		"no path":           "message Spec { repeated string args = 1; }\n",
		"extra field":       "message Spec { string path = 1; string shell = 2; }\n",
		"args not repeated": "message Spec { string path = 1; string args = 2; }\n",
		"env as strings":    "message Spec { string path = 1; repeated string env = 2; }\n",
		"env wrong pair":    "message Kv { string key = 1; string value = 2; }\nmessage Spec { string path = 1; repeated Kv env = 2; }\n",
	}
	for name, spec := range cases {
		pf, err := parseProto(head + spec + run)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		if _, _, err := enrichProto(&pf); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	pf, err := parseProto(head + "message Spec { string path = 1; }\n" + run)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := enrichProto(&pf); err != nil {
		t.Errorf("a spec with only path was refused: %v", err)
	}
}

func TestSurfaceCarriesTheCLIFlags(t *testing.T) {
	pf := cliService(t, `
  // Lists accounts.
  // @cli headless
  rpc List (Req) returns (Res);

  // Starts claude.
  // @cli exec arg=name
  rpc Run (Req) returns (ExecSpec);
`)
	if _, _, err := enrichProto(&pf); err != nil {
		t.Fatal(err)
	}
	cmds := ipcSurfaceOf(pf).Services[0].Commands
	if !cmds[0].CLI || !cmds[0].CLIHeadless || cmds[0].CLIExec {
		t.Errorf("list: %+v", cmds[0])
	}
	if !cmds[1].CLIExec || cmds[1].CLIArg != "name" || cmds[1].CLIHeadless {
		t.Errorf("run: %+v", cmds[1])
	}
}

// Otherwise every app shows drift the day this ships.
func TestIPCSurfaceDocHasNoCLIColumnWithoutCLI(t *testing.T) {
	render := func(body string) string {
		pf := cliService(t, body)
		if _, _, err := enrichProto(&pf); err != nil {
			t.Fatal(err)
		}
		data := protoTemplateData{Proto: pf, Services: pf.Services}
		for _, s := range pf.Services {
			for _, r := range s.RPCs {
				data.HasCLI = data.HasCLI || r.CLI
			}
		}
		staged, err := renderGeneratedFile(generatedFile{Path: "ipc-surface.md", Template: mustRead(template.DocsIPCSurface), Data: data})
		if err != nil {
			t.Fatal(err)
		}
		return string(staged.Content)
	}
	plain := render("  rpc Quiet (Req) returns (Res);\n")
	if strings.Contains(plain, "CLI") {
		t.Errorf("a CLI column appeared in an app without @cli:\n%s", plain)
	}
	with := render("  // Lists.\n  // @cli headless\n  rpc List (Req) returns (Res);\n")
	if !strings.Contains(with, "| CLI |") || !strings.Contains(with, "yes, headless |") {
		t.Errorf("CLI column missing:\n%s", with)
	}
}

func TestHandlerRegistersCLICommandsOnlyWhenAsked(t *testing.T) {
	render := func(body string) string {
		pf := cliService(t, body)
		if _, _, err := enrichProto(&pf); err != nil {
			t.Fatal(err)
		}
		data := protoTemplateData{Module: "probe", Proto: pf, Services: pf.Services}
		for _, s := range pf.Services {
			for _, r := range s.RPCs {
				data.HasCLI = data.HasCLI || r.CLI
			}
		}
		staged, err := renderGeneratedFile(generatedFile{Path: "handler.go", Template: mustRead(template.GoHandler), Data: data})
		if err != nil {
			t.Fatal(err)
		}
		return string(staged.Content)
	}
	plain := render("  rpc Quiet (Req) returns (Res);\n")
	if strings.Contains(plain, "CLICommand") || strings.Contains(plain, "CLIWords") {
		t.Errorf("an app without @cli changed:\n%s", plain)
	}
	got := render("  // Starts claude.\n  // @cli exec headless arg=name rest=tags\n  rpc Run (Req) returns (ExecSpec);\n")
	for _, want := range []string{`Command: "account:run"`, `Service: "account"`, `Arg: "name"`, `Rest: "tags"`, "Headless: true", "Exec: true", `var CLIWords = []string{`, `"account",`} {
		if !strings.Contains(got, want) {
			t.Errorf("handler lacks %s:\n%s", want, got)
		}
	}
}

func TestMainTemplateWiresTheCLIOnlyWhenThereIsOne(t *testing.T) {
	render := func(hasCLI bool) string {
		staged, err := renderGeneratedFile(generatedFile{Path: "main.go", Template: mustRead(template.GoMain), Data: protoTemplateData{Module: "probe", HasCLI: hasCLI}, Go: true})
		if err != nil {
			t.Fatal(err)
		}
		return string(staged.Content)
	}
	if plain := render(false); strings.Contains(plain, "RunCLI(") || strings.Contains(plain, `"os"`) {
		t.Errorf("an app without @cli got the command line wiring:\n%s", plain)
	}
	if with := render(true); !strings.Contains(with, "app.RunCLI(manifest, os.Args[1:], setup)") || !strings.Contains(with, "handler.CLIWords") {
		t.Errorf("an app with @cli is not wired:\n%s", with)
	}
}

func TestCLIModifiersUseJSONNames(t *testing.T) {
	pf, err := parseProto(`syntax = "proto3";
package p;
message Req { string chatId = 1; repeated string extraArgs = 2; string apiKey = 3; }
message Res { string ok = 1; }
service Chat {
  // Doc.
  // @cli arg=chatId rest=extraArgs secret=apiKey
  rpc Send (Req) returns (Res);
}
`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := enrichProto(&pf); err != nil {
		t.Fatal(err)
	}
	r := pf.Services[0].RPCs[0]
	if r.CLIArg != "chat_id" || r.CLIRest != "extra_args" || r.CLISecret != "api_key" {
		t.Errorf("arg %q rest %q secret %q", r.CLIArg, r.CLIRest, r.CLISecret)
	}
}
