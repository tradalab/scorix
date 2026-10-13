package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tradalab/scorix/fault"
	ipc "github.com/tradalab/scorix/internal/ipc"
)

const cliSchema = `{"type":"object","properties":{"name":{"type":"string"},"key":{"type":"string"},"id":{"type":"integer"},"limit":{"type":"integer"},"force":{"type":"boolean"},"tag":{"type":"array","items":{"type":"string"}},"args":{"type":"array","items":{"type":"string"}},"nested":{"type":"object"}},"additionalProperties":false}`

// A struct, as generated types are: a map marshals its keys sorted.
type acct struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type cliRig struct {
	a    *App
	got  map[string]json.RawMessage // command -> request it received
	hung chan struct{}
	out  bytes.Buffer
	errs bytes.Buffer
}

func newCLIRig(t *testing.T) *cliRig {
	t.Helper()
	// Otherwise a running app named probe turns every test into an attach.
	if mcpDirOverride == "" {
		mcpDirOverride = t.TempDir()
		t.Cleanup(func() { mcpDirOverride = "" })
	}
	a, err := New(Options{Manifest: []byte("app:\n  name: probe\n  version: 1.2.3\ncli:\n  enabled: true\n")})
	if err != nil {
		t.Fatal(err)
	}
	r := &cliRig{a: a, got: map[string]json.RawMessage{}, hung: make(chan struct{}, 1)}
	add := func(c CLICommand, reply any, fail error) {
		a.Command(c.Command, func(ctx context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
			r.got[c.Command] = data
			if !CLICall(ctx) {
				return nil, fault.New(fault.CodeInternal, "handler did not see it was called from the command line")
			}
			if c.Command == "account:slow" || c.Command == "account:hang" {
				<-ctx.Done()
				if c.Command == "account:hang" {
					r.hung <- struct{}{}
				}
				return nil, ctx.Err()
			}
			return reply, fail
		})
		c.Schema = json.RawMessage(cliSchema)
		a.CLICommand(c)
	}
	add(CLICommand{Command: "account:list", Service: "account", Doc: "Lists accounts.", Headless: true},
		struct {
			Accounts []acct `json:"accounts"`
		}{[]acct{{"work", "subscription"}, {"home", "api"}}}, nil)
	add(CLICommand{Command: "account:show", Service: "account", Doc: "Shows one.", Headless: true, Arg: "name", Rest: "args"},
		map[string]any{"name": "work"}, nil)
	add(CLICommand{Command: "account:remove", Service: "account", Doc: "Removes one.", Headless: true, Destructive: true, Arg: "name"}, nil, nil)
	add(CLICommand{Command: "account:attach", Service: "account", Doc: "Needs the app open."}, nil, nil)
	add(CLICommand{Command: "account:missing", Service: "account", Doc: "Not found.", Headless: true}, nil, fault.New(fault.CodeNotFound, "no such account"))
	add(CLICommand{Command: "account:slow", Service: "account", Doc: "Never ends.", Headless: true, Timeout: 50 * time.Millisecond}, nil, nil)
	add(CLICommand{Command: "account:hang", Service: "account", Doc: "Waits until cancelled."}, nil, nil)
	add(CLICommand{Command: "account:key", Service: "account", Doc: "Takes a key.", Headless: true, Arg: "name", Secret: "key"}, nil, nil)
	add(CLICommand{Command: "account:get", Service: "account", Doc: "By number.", Headless: true, Arg: "id"}, nil, nil)
	type line struct {
		Line string `json:"line"`
	}
	RegisterServerStream(a, "account:tail", func(ctx context.Context, req *struct {
		Fail bool `json:"fail"`
	}, out Sink[line]) error {
		for _, l := range []string{"a", "b", "c"} {
			if err := out.Send(&line{l}); err != nil {
				return err
			}
		}
		if req.Fail {
			return fault.New(fault.CodeNotFound, "gone mid-stream")
		}
		return nil
	})
	a.CLICommand(CLICommand{Command: "account:tail", Service: "account", Doc: "Streams lines.", Headless: true, Stream: true,
		Schema: json.RawMessage(`{"type":"object","properties":{"fail":{"type":"boolean"}}}`)})
	a.Command("account:secret", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		r.got["account:secret"] = nil
		return nil, nil
	})
	return r
}

func (r *cliRig) run(tty bool, stdin string, args ...string) int {
	r.out.Reset()
	r.errs.Reset()
	return r.a.runCLI(args, cliIO{in: strings.NewReader(stdin), out: &r.out, err: &r.errs, tty: tty})
}

func (r *cliRig) request(t *testing.T, cmd string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.got[cmd], &m); err != nil {
		t.Fatalf("%s got %s: %v", cmd, r.got[cmd], err)
	}
	return m
}

func TestIsCLIOnlyByTheFirstWord(t *testing.T) {
	words := []string{"account"}
	for _, args := range [][]string{{"account", "list"}, {"call", "x:y"}, {"help"}, {"--version"}} {
		if !IsCLI(args, words) {
			t.Errorf("%v is a command line", args)
		}
	}
	for _, args := range [][]string{nil, {"/home/u/notes.md"}, {"loom://open"}, {"-mode", "web"}, {"list", "account"}} {
		if IsCLI(args, words) {
			t.Errorf("%v opened a command line instead of the app", args)
		}
	}
}

func TestFlagsPositionalAndRestBuildTheRequest(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "account", "show", "work", "--limit", "5", "--force", "--tag", "a", "--tag=b", "--", "--model", "opus"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, r.errs.String())
	}
	got := r.request(t, "account:show")
	if got["name"] != "work" || got["limit"] != float64(5) || got["force"] != true {
		t.Errorf("request = %v", got)
	}
	if tags, _ := got["tag"].([]any); len(tags) != 2 || tags[1] != "b" {
		t.Errorf("repeatable flag = %v", got["tag"])
	}
	if rest, _ := got["args"].([]any); len(rest) != 2 || rest[0] != "--model" {
		t.Errorf("words after -- = %v; they must pass through untouched", got["args"])
	}
}

func TestBadUsageIsExit2AndCallsNothing(t *testing.T) {
	for _, args := range [][]string{
		{"account", "show", "--nope", "x"},
		{"account", "show", "--limit", "five"},
		{"account", "show", "--nested", "{}"},
		{"account", "show", "a", "b"},
		{"account", "list", "extra"},
		{"account", "list", "--", "x"},
		{"account", "nothing"},
	} {
		r := newCLIRig(t)
		if code := r.run(false, "", args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
		if len(r.got) != 0 {
			t.Errorf("%v reached a handler: %v", args, r.got)
		}
	}
}

func TestCallReachesOnlyCLICommands(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "call", "account:secret", "{}"); code != ExitDenied {
		t.Errorf("call on a command without @cli: exit %d, want %d", code, ExitDenied)
	}
	if _, ran := r.got["account:secret"]; ran {
		t.Fatal("call ran a command the annotation never offered")
	}
	if code := r.run(false, `{"name":"work","limit":2}`, "call", "account:show", "-"); code != ExitOK {
		t.Fatalf("call from stdin: exit %d: %s", code, r.errs.String())
	}
	if got := r.request(t, "account:show"); got["limit"] != float64(2) {
		t.Errorf("request = %v", got)
	}
}

func TestDestructiveAsksOrNeedsYes(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "account", "remove", "work"); code != ExitDenied {
		t.Errorf("no terminal, no --yes: exit %d", code)
	}
	// A "y" piped into a script is not a person answering.
	if code := r.run(false, "y\n", "account", "remove", "work"); code != ExitDenied {
		t.Errorf("piped y without a terminal: exit %d", code)
	}
	if code := r.run(true, "n\n", "account", "remove", "work"); code != ExitDenied {
		t.Errorf("answered n: exit %d", code)
	}
	if _, ran := r.got["account:remove"]; ran {
		t.Fatal("ran without a yes")
	}
	if code := r.run(true, "y\n", "account", "remove", "work"); code != ExitOK {
		t.Errorf("answered y: exit %d", code)
	}
	delete(r.got, "account:remove")
	if code := r.run(false, "", "account", "remove", "work", "--yes"); code != ExitOK {
		t.Errorf("--yes: exit %d", code)
	}
	if _, ran := r.got["account:remove"]; !ran {
		t.Error("--yes did not run it")
	}
}

func TestExitCodes(t *testing.T) {
	r := newCLIRig(t)
	cases := []struct {
		args []string
		want int
	}{
		{[]string{"account", "attach"}, ExitUnavailable},
		{[]string{"account", "missing"}, ExitNotFound},
		{[]string{"account", "slow"}, ExitTimeout},
	}
	for _, c := range cases {
		if code := r.run(false, "", c.args...); code != c.want {
			t.Errorf("%v: exit %d, want %d (%s)", c.args, code, c.want, r.errs.String())
		}
	}
	if _, ran := r.got["account:attach"]; ran {
		t.Error("a command without headless ran with no app open")
	}
}

func TestOutputKeepsTheReplyOrder(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "account", "list"); code != ExitOK {
		t.Fatal(r.errs.String())
	}
	lines := strings.Split(strings.TrimSpace(r.out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") || !strings.HasPrefix(lines[1], "work") {
		t.Errorf("table:\n%s", r.out.String())
	}
	r.run(false, "", "account", "list", "--json")
	var v map[string]any
	if err := json.Unmarshal(r.out.Bytes(), &v); err != nil {
		t.Errorf("--json is not JSON: %q", r.out.String())
	}
}

func TestHelpAndVersion(t *testing.T) {
	r := newCLIRig(t)
	r.run(false, "", "help")
	if !strings.Contains(r.out.String(), "account list") || strings.Contains(r.out.String(), "secret") {
		t.Errorf("help:\n%s", r.out.String())
	}
	r.run(false, "", "help", "account", "show")
	if !strings.Contains(r.out.String(), "account show <name> [-- <args>...]") || !strings.Contains(r.out.String(), "--limit (integer)") {
		t.Errorf("help for one command:\n%s", r.out.String())
	}
	for _, args := range [][]string{{"account", "show", "--help"}, {"account", "show", "-h"}, {"--help", "account", "show"}} {
		if code := r.run(false, "", args...); code != ExitOK || !strings.Contains(r.out.String(), "account show <name>") {
			t.Errorf("%v = %d:\n%s%s", args, code, r.out.String(), r.errs.String())
		}
	}
	r.run(false, "", "--version")
	if strings.TrimSpace(r.out.String()) != "probe 1.2.3" {
		t.Errorf("version = %q", r.out.String())
	}
}

func TestExecEnvUnsetsAndSets(t *testing.T) {
	spec := ExecSpec{Unset: []string{"ANTHROPIC_API_KEY"}, Env: []EnvVar{{"CLAUDE_CONFIG_DIR", "/acct"}}}
	got := execEnv([]string{"PATH=/bin", "ANTHROPIC_API_KEY=stray", "CLAUDE_CONFIG_DIR=/old"}, spec)
	want := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/acct"}
	if !slices.Equal(got, want) {
		t.Errorf("env = %v, want %v", got, want)
	}
}

// The exec would replace the test process, so a child runs it.
func TestExecBecomesTheProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix sh")
	}
	if os.Getenv("SCORIX_CLI_EXEC_CHILD") == "1" {
		os.Exit(runExec(ExecSpec{
			Path:  "sh",
			Args:  []string{"-c", `echo "dir=$CLAUDE_CONFIG_DIR key=$ANTHROPIC_API_KEY"; exit 3`},
			Env:   []EnvVar{{"CLAUDE_CONFIG_DIR", "/acct"}},
			Unset: []string{"ANTHROPIC_API_KEY"},
		}, os.Stderr))
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecBecomesTheProgram$")
	cmd.Env = append(os.Environ(), "SCORIX_CLI_EXEC_CHILD=1", "ANTHROPIC_API_KEY=stray")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if !errAs(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("exit = %v, want 3 from the program", err)
	}
	if got := strings.TrimSpace(string(out)); got != "dir=/acct key=" {
		t.Errorf("program saw %q", got)
	}
}

func errAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
}

func TestDevNullIsNotATerminal(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Fatal("stdin from /dev/null counted as a person at a terminal, so cron would be asked y/N")
	}
}

func TestOneTextFieldPrintsAsText(t *testing.T) {
	var out bytes.Buffer
	renderHuman(&out, json.RawMessage(`{"script":"export A='1';\n"}`))
	if out.String() != "export A='1';\n" {
		t.Errorf("got %q; eval would choke on anything but the text", out.String())
	}
}

func TestSecretComesFromStdinOnly(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "account", "key", "acme", "--key", "sk-typed"); code != ExitUsage {
		t.Errorf("a key typed on the command line: exit %d", code)
	}
	if _, ran := r.got["account:key"]; ran {
		t.Fatal("ran with a key from the command line")
	}
	if code := r.run(false, "sk-piped\n", "account", "key", "acme", "--key", "-"); code != ExitOK {
		t.Fatalf("--key -: exit %d %s", code, r.errs.String())
	}
	if got := r.request(t, "account:key"); got["key"] != "sk-piped" || got["name"] != "acme" {
		t.Errorf("request = %v", got)
	}
	if strings.Contains(r.errs.String()+r.out.String(), "sk-piped") {
		t.Error("the key was echoed")
	}
	delete(r.got, "account:key")
	if code := r.run(false, "", "account", "key", "acme", "--key", "-"); code != ExitUsage {
		t.Errorf("nothing on stdin: exit %d", code)
	}
}

// Pinned to cross-spawn@7.0.6's output for this input; not yet run on Windows.
func TestCmdLineEscapesWhatCmdExeActsOn(t *testing.T) {
	got := cmdLine(`C:\Program Files\nodejs\claude.cmd`, []string{"-p", `fix A & B`, `50%`, `say "hi"`, `C:\dir\`})
	want := `/d /s /c "C:\Program^ Files\nodejs\claude.cmd ^"-p^" ^"fix^ A^ ^&^ B^" ^"50^%^" ^"say^ \^"hi\^"^" ^"C:\dir\\^""`
	if got != want {
		t.Errorf("cmdLine:\n got %s\nwant %s", got, want)
	}
}

func TestPositionalIsTypedLikeItsFlag(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "account", "get", "7"); code != ExitOK {
		t.Fatalf("exit %d %s", code, r.errs.String())
	}
	if got := r.request(t, "account:get"); got["id"] != float64(7) {
		t.Errorf("id = %#v; a string would fail the handler's unmarshal into an int", got["id"])
	}
	if code := r.run(false, "", "account", "get", "seven"); code != ExitUsage {
		t.Errorf("a non-number: exit %d", code)
	}
}

func TestCallRefusesASecretOnTheCommandLine(t *testing.T) {
	r := newCLIRig(t)
	if code := r.run(false, "", "call", "account:key", `{"name":"a","key":"sk-typed"}`); code != ExitUsage {
		t.Errorf("call with a key in argv: exit %d", code)
	}
	if code := r.run(false, "", "call", "account:key", `{"name":"a","KEY":"sk-typed"}`); code != ExitUsage {
		t.Errorf("call with the key spelled KEY: exit %d; json.Unmarshal still hands it to the field", code)
	}
	if _, ran := r.got["account:key"]; ran {
		t.Fatal("ran with a key from argv")
	}
	if code := r.run(false, `{"name":"a","key":"sk-piped"}`, "call", "account:key", "-"); code != ExitOK {
		t.Errorf("call with the JSON on stdin: exit %d %s", code, r.errs.String())
	}
}

func TestSameAppName(t *testing.T) {
	for _, c := range []struct {
		base, app string
		want      bool
	}{
		{"Loom.AppImage", "Loom", true},
		{"Loom-1.7.0-linux-amd64.AppImage", "Loom", true},
		{"My App", "My App", true},
		{"My_App-2.0.AppImage", "My App", true},
		{"LoomPro.AppImage", "Loom", false},
		{"code", "Loom", false},
		{"Gopher", "Go", false},
	} {
		if got := sameAppName(c.base, c.app); got != c.want {
			t.Errorf("sameAppName(%q, %q) = %v", c.base, c.app, got)
		}
	}
}
