package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ipc "github.com/tradalab/scorix/internal/ipc"
)

func attachPair(t *testing.T) (server, client *cliRig) {
	t.Helper()
	server = newCLIRig(t)
	client = newCLIRig(t)
	server.a.openCLI()
	t.Cleanup(server.a.closeCLI)
	return server, client
}

func TestAttachRunsTheCommandInTheOpenApp(t *testing.T) {
	server, client := attachPair(t)
	if code := client.run(false, "", "account", "attach"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, client.errs.String())
	}
	if _, ran := server.got["account:attach"]; !ran {
		t.Fatal("the open app did not run it")
	}
	if _, ran := client.got["account:attach"]; ran {
		t.Fatal("the command line ran it itself instead of attaching")
	}
	if code := client.run(false, "", "account", "list"); code != ExitOK || !strings.Contains(client.out.String(), "work") {
		t.Errorf("list over attach: exit %d out %q", code, client.out.String())
	}
	if code := client.run(false, "", "account", "missing"); code != ExitNotFound {
		t.Errorf("fault code over the socket: exit %d, want %d", code, ExitNotFound)
	}
}

func rawRequest(t *testing.T, req cliRequest) cliReply {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(mcpDirOverride, cliEndpointFile))
	if err != nil {
		t.Fatal(err)
	}
	var ep mcpEndpoint
	if err := json.Unmarshal(b, &ep); err != nil {
		t.Fatal(err)
	}
	if req.Token == "" {
		req.Token = ep.Token
	}
	conn, err := net.Dial("unix", ep.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var rep cliReply
	if err := json.Unmarshal(reply, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestAttachServerChecksForItself(t *testing.T) {
	server, _ := attachPair(t)
	if rep := rawRequest(t, cliRequest{Command: "account:remove", Args: json.RawMessage(`{"name":"work"}`)}); rep.OK {
		t.Error("an unconfirmed destructive command ran")
	}
	if rep := rawRequest(t, cliRequest{Command: "account:secret"}); rep.OK {
		t.Error("the socket reached a command without @cli")
	}
	if rep := rawRequest(t, cliRequest{Token: "wrong", Command: "account:list"}); rep.OK {
		t.Error("a wrong token was accepted")
	}
	for _, cmd := range []string{"account:remove", "account:secret", "account:list"} {
		if _, ran := server.got[cmd]; ran {
			t.Errorf("%s reached its handler", cmd)
		}
	}
	if rep := rawRequest(t, cliRequest{Command: "account:remove", Args: json.RawMessage(`{"name":"work"}`), Confirmed: true}); !rep.OK {
		t.Errorf("a confirmed destructive command was refused: %+v", rep)
	}
}

func TestStaleEndpointFallsBackToHeadless(t *testing.T) {
	client := newCLIRig(t)
	dead := filepath.Join(mcpDirOverride, "gone.sock")
	if err := writeJSON0600(filepath.Join(mcpDirOverride, cliEndpointFile), mcpEndpoint{Addr: dead, Token: "x"}); err != nil {
		t.Fatal(err)
	}
	if code := client.run(false, "", "account", "list"); code != ExitOK {
		t.Fatalf("a crashed app's endpoint blocked a headless command: exit %d %s", code, client.errs.String())
	}
	if _, ran := client.got["account:list"]; !ran {
		t.Error("did not fall back to running it here")
	}
}

func TestHangingUpCancelsTheCommand(t *testing.T) {
	server, client := attachPair(t)
	// account:hang has no timeout of its own: only the hang-up can end it.
	if code := client.run(false, "", "account", "hang", "--timeout", "100ms"); code != ExitTimeout {
		t.Errorf("exit %d, want %d", code, ExitTimeout)
	}
	select {
	case <-server.hung:
	case <-time.After(3 * time.Second):
		t.Fatal("the command kept running in the app after the command line hung up")
	}
}

func TestClosingRemovesTheEndpoint(t *testing.T) {
	server := newCLIRig(t)
	server.a.openCLI()
	path := filepath.Join(mcpDirOverride, cliEndpointFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no endpoint while open: %v", err)
	}
	server.a.closeCLI()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("endpoint left behind: %v", err)
	}
}

func TestOpeningClearsADeadInstancesSocket(t *testing.T) {
	server := newCLIRig(t)
	dead := filepath.Join(mcpDirOverride, "cli-dead.sock")
	if err := os.WriteFile(dead, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON0600(filepath.Join(mcpDirOverride, cliEndpointFile), mcpEndpoint{Addr: dead, Token: "x"}); err != nil {
		t.Fatal(err)
	}
	server.a.openCLI()
	defer server.a.closeCLI()
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the crashed instance's socket is still there: %v", err)
	}
}

func TestStreamPrintsEachChunkAsItComes(t *testing.T) {
	check := func(name string, r *cliRig) {
		t.Helper()
		if code := r.run(false, "", "account", "tail"); code != ExitOK || r.out.String() != "a\nb\nc\n" {
			t.Errorf("%s: exit %d out %q err %q", name, code, r.out.String(), r.errs.String())
		}
		r.run(false, "", "account", "tail", "--json")
		if lines := strings.Split(strings.TrimSpace(r.out.String()), "\n"); len(lines) != 3 || lines[0] != `{"line":"a"}` {
			t.Errorf("%s --json: %q", name, r.out.String())
		}
		if code := r.run(false, "", "account", "tail", "--fail"); code != ExitNotFound || r.out.String() != "a\nb\nc\n" {
			t.Errorf("%s failing: exit %d out %q; the chunks before the error still count", name, code, r.out.String())
		}
	}
	check("headless", newCLIRig(t))
	_, client := attachPair(t)
	check("attached", client)
}

func TestLaunchStartsTheAppThenAttaches(t *testing.T) {
	server, client := newCLIRig(t), newCLIRig(t)
	launched := false
	was := cliLaunch
	cliLaunch = func() error {
		launched = true
		go func() {
			time.Sleep(100 * time.Millisecond)
			server.a.openCLI()
		}()
		return nil
	}
	t.Cleanup(func() { cliLaunch = was; server.a.closeCLI() })

	if code := client.run(false, "", "account", "attach"); code != ExitUnavailable || launched {
		t.Fatalf("without --launch: exit %d launched %v", code, launched)
	}
	if !strings.Contains(client.errs.String(), "--launch") {
		t.Errorf("the refusal does not mention --launch: %q", client.errs.String())
	}
	if code := client.run(false, "", "account", "attach", "--launch"); code != ExitOK {
		t.Fatalf("--launch: exit %d: %s", code, client.errs.String())
	}
	if _, ran := server.got["account:attach"]; !launched || !ran {
		t.Errorf("launched %v, ran in the app %v", launched, ran)
	}
}

func TestExecAnswersOnlyTheCommandLine(t *testing.T) {
	r := newCLIRig(t)
	r.a.Command("claude:run", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		return map[string]any{"path": "sh", "env": []map[string]string{{"name": "ANTHROPIC_API_KEY", "value": "sk-secret"}}}, nil
	})
	r.a.CLICommand(CLICommand{Command: "claude:run", Service: "claude", Doc: "Runs.", Headless: true, Exec: true, Schema: json.RawMessage(`{}`)})
	res, err := r.a.reg.Invoke(context.Background(), "claude:run", nil)
	if err == nil || strings.Contains(string(res), "sk-secret") {
		t.Fatalf("an IPC caller got the exec reply: %s %v", res, err)
	}
	ctx := context.WithValue(context.Background(), cliCallKey{}, true)
	if res, err := r.a.reg.Invoke(ctx, "claude:run", nil); err != nil || !strings.Contains(string(res), "sk-secret") {
		t.Fatalf("the command line was refused: %s %v", res, err)
	}
}

func TestClientTimeoutWinsWhenAttached(t *testing.T) {
	_, client := attachPair(t)
	start := time.Now()
	client.run(false, "", "account", "slow", "--timeout", "400ms")
	if time.Since(start) < 300*time.Millisecond {
		t.Errorf("ended after %s: the server applied the command's 50ms, not the client's 400ms", time.Since(start))
	}
}

func TestAppClosingIsUnavailableNotDeclined(t *testing.T) {
	server, client := attachPair(t)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		time.Sleep(100 * time.Millisecond)
		server.a.closeCLI()
	}()
	if code := client.run(false, "", "account", "hang"); code != ExitUnavailable {
		t.Errorf("exit %d, want %d: the app went away, nobody said no", code, ExitUnavailable)
	}
	// closeCLI still reads the data dir; Cleanup must not reset it under it.
	<-closed
}

func TestASecondInstanceKeepsTheFirstsEndpoint(t *testing.T) {
	first, second := newCLIRig(t), newCLIRig(t)
	first.a.openCLI()
	defer first.a.closeCLI()
	path := filepath.Join(mcpDirOverride, cliEndpointFile)
	before, _ := os.ReadFile(path)
	second.a.openCLI()
	defer second.a.closeCLI()
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("the second instance took over the endpoint; the first becomes unreachable once it exits")
	}
}

func TestExecGuardSurvivesRegistrationOrder(t *testing.T) {
	r := newCLIRig(t)
	leak := func(context.Context, json.RawMessage, ipc.Stream) (any, error) { return "sk-secret", nil }
	r.a.CLICommand(CLICommand{Command: "x:run", Service: "x", Doc: "Runs.", Headless: true, Exec: true, Schema: json.RawMessage(`{}`)})
	r.a.Command("x:run", leak)
	if res, err := r.a.reg.Invoke(context.Background(), "x:run", nil); err == nil {
		t.Errorf("registered after the guard, reached IPC: %s", res)
	}
	r.a.Command("x:run", leak)
	if res, err := r.a.reg.Invoke(context.Background(), "x:run", nil); err == nil {
		t.Errorf("registered again, reached IPC: %s", res)
	}
}

func TestSecondInstanceTakesOverWhenTheFirstExits(t *testing.T) {
	was := cliTakeoverEvery
	cliTakeoverEvery = 20 * time.Millisecond
	t.Cleanup(func() { cliTakeoverEvery = was })
	first, second := newCLIRig(t), newCLIRig(t)
	first.a.openCLI()
	second.a.openCLI()
	t.Cleanup(second.a.closeCLI)
	first.a.closeCLI()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cliEndpointLive(mcpDirOverride) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the second instance never opened the command line after the first exited")
}

func TestLateWebSocketIsRefusedAfterShutdown(t *testing.T) {
	a, err := New(Options{URL: "scorix://app/index.html"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.Handler())
	defer ts.Close()
	defer a.stopModules()
	a.closeWebSockets()
	c := dialWS(t, ts)
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("a connection opened after shutdown stayed open")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("a connection opened after shutdown stayed open until the deadline")
	}
	a.wsMu.Lock()
	n := len(a.wsConns)
	a.wsMu.Unlock()
	if n != 0 {
		t.Errorf("%d late connections tracked", n)
	}
}
