package app

import (
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/internal/devctl"
	"github.com/tradalab/scorix/webview"
)

// Turns the socket on for one test and points it at a temp file: the real path
// is under the user's profile, which a test must not write to.
func devControlIn(t *testing.T, path string) {
	t.Helper()
	t.Setenv(devctl.Env, "1")
	devControlPathOverride = path
	t.Cleanup(func() { devControlPathOverride = "" })
}

// Reads the published address back the way the CLI does.
func devControlAt(t *testing.T) (*App, *httptest.Server, devctl.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "devctl.json")
	devControlIn(t, path)

	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler()) // Handler runs startModules, which opens the socket
	t.Cleanup(ts.Close)
	t.Cleanup(a.stopModules)

	return a, ts, readControlFile(t, path)
}

func readControlFile(t *testing.T, path string) devctl.File {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no control file was published: %v", err)
	}
	var f devctl.File
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.Addr == "" || f.Token == "" || f.PID == 0 {
		t.Fatalf("control file is incomplete: %s", b)
	}
	return f
}

func devAsk(t *testing.T, f devctl.File, req devRequest) devResponse {
	t.Helper()
	conn, err := net.DialTimeout("tcp", f.Addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", f.Addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		t.Fatal(err)
	}
	var res devResponse
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		t.Fatalf("no answer: %v", err)
	}
	return res
}

func TestDevControlStaysShutWithoutTheEnv(t *testing.T) {
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	defer a.stopModules()

	a.mu.Lock()
	dc := a.devctl
	a.mu.Unlock()
	if dc != nil {
		t.Fatal("the control socket opened with no SCORIX_DEV_CONTROL: a shipped build would carry it")
	}
}

func TestDevControlRefusesABadToken(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token + "x", Op: "status"})
	if res.OK || !strings.Contains(res.Error, "unauthorized") {
		t.Fatalf("a wrong token was served: %+v", res)
	}
	if strings.Contains(res.Error, f.Token) {
		t.Error("the refusal echoes the real token back")
	}
	// An empty token must read the same as a wrong one.
	if res2 := devAsk(t, f, devRequest{Op: "status"}); res2.Error != res.Error {
		t.Errorf("no token (%q) and a wrong token (%q) answer differently", res2.Error, res.Error)
	}
}

func TestDevControlRefusesAnUnknownOp(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "rm_rf"})
	if res.OK || !strings.Contains(res.Error, "unknown op") {
		t.Fatalf("an unknown op was accepted: %+v", res)
	}
}

func TestDevControlCallInvokesABoundCommand(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "call",
		Args: json.RawMessage(`{"name":"echo","payload":"hi"}`)})
	if !res.OK {
		t.Fatalf("call failed: %+v", res)
	}
	if got := strings.TrimSpace(string(res.Data)); got != `"echo:hi"` {
		t.Fatalf("data = %s, want \"echo:hi\"", got)
	}
}

func TestDevControlCallRefusesAnUnboundName(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "call", Args: json.RawMessage(`{"name":"nope:missing"}`)})
	if res.OK {
		t.Fatal("an unbound command answered")
	}
}

func TestDevControlListsBoundCommands(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "commands"})
	if !res.OK {
		t.Fatalf("commands failed: %+v", res)
	}
	var names []string
	if err := json.Unmarshal(res.Data, &names); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(names, "echo") || !slices.Contains(names, "count") {
		t.Fatalf("the bound commands are missing from %v", names)
	}
	if !slices.IsSorted(names) {
		t.Errorf("names are not sorted, so the list reorders between calls: %v", names)
	}
}

func TestDevControlEvalReachesTheFrontend(t *testing.T) {
	a, ts, f := devControlAt(t)

	var asked webview.Message
	startCallEcho(t, a, ts, func(m webview.Message) *webview.Message {
		asked = m
		out, _ := json.Marshal(map[string]any{"title": "hi"})
		return &webview.Message{ID: m.ID, Kind: "callreply", Name: m.Name, State: "done", Data: out}
	})

	res := devAsk(t, f, devRequest{Token: f.Token, Op: "eval", Args: json.RawMessage(`{"code":"document.title"}`)})
	if !res.OK {
		t.Fatalf("eval failed: %+v", res)
	}
	if asked.Name != "dev:eval" {
		t.Fatalf("the frontend was asked for %q, want dev:eval", asked.Name)
	}
	if !strings.Contains(string(asked.Data), "document.title") {
		t.Errorf("the code never reached the frontend: %s", asked.Data)
	}
	if !strings.Contains(string(res.Data), "hi") {
		t.Errorf("the frontend's answer never came back: %s", res.Data)
	}
}

// A frontend that is connected but never answers must fail the call, not wedge
// the socket: the timeout is the only thing between an agent and a dead session.
func TestDevControlJSOpTimesOutWhenTheFrontendIsSilent(t *testing.T) {
	a, ts, f := devControlAt(t)
	startCallEcho(t, a, ts, func(webview.Message) *webview.Message { return nil })

	start := time.Now()
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "dom", TimeoutMS: 300,
		Args: json.RawMessage(`{"selector":"body"}`)})
	if res.OK {
		t.Fatal("a silent frontend answered")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the call hung for %s: the deadline is not being applied", elapsed)
	}
}

// Measured: time.Duration(1<<62) * time.Millisecond wraps to 0s, so min() keeps
// the zero and the call is cancelled before it runs. A silly timeout_ms has to
// land on the ceiling, not collapse to nothing.
func TestDevControlHugeTimeoutClampsInsteadOfCancelling(t *testing.T) {
	a, ts, f := devControlAt(t)
	startCallEcho(t, a, ts, func(m webview.Message) *webview.Message {
		return &webview.Message{ID: m.ID, Kind: "callreply", Name: m.Name, State: "done", Data: json.RawMessage(`"ok"`)}
	})

	res := devAsk(t, f, devRequest{Token: f.Token, Op: "eval", TimeoutMS: 1 << 62,
		Args: json.RawMessage(`{"code":"1"}`)})
	if !res.OK {
		t.Fatalf("a huge timeout_ms cancelled the call instead of clamping: %+v", res)
	}
}

func TestDevControlRefusesJSOpsWithNoFrontend(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "eval", Args: json.RawMessage(`{"code":"1"}`)})
	if res.OK || !strings.Contains(res.Error, "no frontend") {
		t.Fatalf("eval with nothing connected: %+v", res)
	}
}

func TestDevControlFileIsPrivateAndRemovedOnStop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devctl.json")
	devControlIn(t, path)
	a := newTestApp(t)
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()

	if runtime.GOOS != "windows" { // Windows perm bits do not carry the same meaning
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := st.Mode().Perm(); perm != 0o600 {
			t.Errorf("control file is %o, want 600: it holds the token", perm)
		}
	}

	a.stopModules()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the control file outlived the app, so the next call dials a dead port")
	}
}

// The only op that touches the native runtime, so it is the only one that needs a
// real window rather than the web-mode harness.
func TestDevControlWindowDrivesTheRealWindow(t *testing.T) {
	withHeadlessDriver(t)
	path := filepath.Join(t.TempDir(), "devctl.json")
	devControlIn(t, path)

	a, err := New(Options{Title: "main", URL: "scorix://app/index.html", Width: 400, Height: 300})
	if err != nil {
		t.Fatal(err)
	}
	a.Serve("scorix", fstest.MapFS{"index.html": {Data: []byte("<html></html>")}})

	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	waitUntil(t, "the main window never opened", func() bool { return a.MainWindow() != nil })
	waitUntil(t, "no control file was published", func() bool { _, err := os.Stat(path); return err == nil })
	f := readControlFile(t, path)

	res := devAsk(t, f, devRequest{Token: f.Token, Op: "window",
		Args: json.RawMessage(`{"action":"resize","w":640,"h":480}`)})
	if !res.OK {
		t.Fatalf("resize failed: %+v", res)
	}
	// Read the window itself, not the answer: the answer could be echoing back
	// what was asked for while the Dispatch hop quietly never ran.
	if w, h := a.MainWindow().Size(); w != 640 || h != 480 {
		t.Fatalf("the window is %dx%d, so the resize did not reach it", w, h)
	}
	if !strings.Contains(string(res.Data), `"w":640`) {
		t.Errorf("the report does not carry the new box: %s", res.Data)
	}

	if bad := devAsk(t, f, devRequest{Token: f.Token, Op: "window", Args: json.RawMessage(`{"action":"levitate"}`)}); bad.OK {
		t.Error("an unknown window action was accepted")
	}
	if bad := devAsk(t, f, devRequest{Token: f.Token, Op: "window", Args: json.RawMessage(`{"action":"resize","w":0,"h":0}`)}); bad.OK {
		t.Error("a zero resize was accepted, which would collapse the window")
	}

	a.Quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Quit")
	}
}

func TestDevControlWindowSaysWebModeHasNone(t *testing.T) {
	_, _, f := devControlAt(t)
	res := devAsk(t, f, devRequest{Token: f.Token, Op: "window"})
	if res.OK || !strings.Contains(res.Error, "no native window") {
		t.Fatalf("web mode answered a window op: %+v", res)
	}
}

func TestDevControlEmitReachesAClient(t *testing.T) {
	_, ts, f := devControlAt(t)
	c := dialWS(t, ts)
	t.Cleanup(func() { c.Close() })

	// Round-trip one command first: the client is only a broadcast target once
	// the upgrade has registered it, and emitting into that gap proves nothing.
	wsSend(t, c, webview.Message{ID: "p", Kind: "command", Name: "echo", State: "start", Data: json.RawMessage(`"x"`)})
	waitUntil(t, "the client never got its command reply", func() bool { return wsRecv(t, c).ID == "p" })

	if res := devAsk(t, f, devRequest{Token: f.Token, Op: "emit",
		Args: json.RawMessage(`{"name":"dev:ping","data":{"n":1}}`)}); !res.OK {
		t.Fatalf("emit failed: %+v", res)
	}

	m := wsRecv(t, c)
	if m.Kind != "event" || m.Name != "dev:ping" {
		t.Fatalf("the client got %s/%s instead of the emitted event", m.Kind, m.Name)
	}
	if !strings.Contains(string(m.Data), `"n":1`) {
		t.Errorf("payload = %s", m.Data)
	}
}

func TestDevControlEmitNeedsAName(t *testing.T) {
	_, _, f := devControlAt(t)
	if res := devAsk(t, f, devRequest{Token: f.Token, Op: "emit", Args: json.RawMessage(`{"data":1}`)}); res.OK {
		t.Fatal("an event with no name was emitted, so every listener missed it silently")
	}
}

func TestDevControlScriptOnlyExistsWhileTheSocketDoes(t *testing.T) {
	if devControlScript() != "" {
		t.Fatal("the dev handlers are injected with no SCORIX_DEV_CONTROL set")
	}
	t.Setenv(devctl.Env, "1")
	if !strings.Contains(devControlScript(), "dev:eval") {
		t.Error("the dev handlers are missing when the socket is open")
	}
}
