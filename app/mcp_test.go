package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/tradalab/scorix/config"
	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/logger"
	"github.com/tradalab/scorix/webview"
)

const sumSchema = `{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}}}`

func mcpApp(t *testing.T, userOn bool) (*App, *atomic.Int32) {
	t.Helper()
	mcpDirOverride = t.TempDir()
	t.Cleanup(func() { mcpDirOverride = "" })

	a := newTestApp(t)
	a.cfg.MCP.Enabled = true
	a.Command("sum", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		var in struct{ A, B int }
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, err
		}
		return map[string]int{"sum": in.A + in.B}, nil
	})
	wiped := &atomic.Int32{}
	a.Command("wipe", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		wiped.Add(1)
		return map[string]bool{"wiped": true}, nil
	})
	a.MCPTool(MCPTool{Name: "sum", Command: "sum", Description: "Adds two numbers.", InputSchema: json.RawMessage(sumSchema)})
	a.MCPTool(MCPTool{Name: "wipe", Command: "wipe", Description: "Deletes everything.", InputSchema: json.RawMessage(`{"type":"object"}`), Destructive: true})
	connectAs(t, testClientPath)
	settings := mcpSettings{Enabled: userOn, ApprovedClients: []mcpApprovedClient{{Path: testClientPath, Name: "claude-ai"}}}
	if err := writeJSON0600(filepath.Join(mcpDirOverride, mcpSettingsFile), settings); err != nil {
		t.Fatal(err)
	}
	return a, wiped
}

// The OS would name whatever ran go test.
var testClientPath = filepath.Join(string(filepath.Separator)+"apps", "claude")

func connectAs(t *testing.T, path string) {
	t.Helper()
	old := mcpIdentify
	mcpIdentify = func(net.Conn) (mcpPeer, error) { return mcpPeer{PID: 1, Path: path}, nil }
	t.Cleanup(func() { mcpIdentify = old })
}

func setMCPUser(t *testing.T, on bool) {
	t.Helper()
	s := loadMCPSettings("")
	s.Enabled = on
	if err := writeJSON0600(filepath.Join(mcpDirOverride, mcpSettingsFile), s); err != nil {
		t.Fatal(err)
	}
}

func hosting(t *testing.T, a *App) {
	t.Helper()
	a.openMCP()
	t.Cleanup(a.closeMCP)
}

func mcpRunning(a *App) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mcp != nil
}

type mcpClient struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Reader
	seq  int
	done chan error
}

func relay(t *testing.T, cfg *config.Config) *mcpClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &mcpClient{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() {
		err := proxyMCP(context.Background(), cfg, inR, outW)
		_ = outW.CloseWithError(io.EOF)
		c.done <- err
	}()
	t.Cleanup(func() { _ = inW.Close() })
	return c
}

func (c *mcpClient) call(method string, params any) map[string]any {
	c.t.Helper()
	c.seq++
	req := map[string]any{"jsonrpc": "2.0", "id": c.seq, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, _ := json.Marshal(req)
	go func() { _, _ = c.in.Write(append(b, '\n')) }()
	type answer struct {
		line []byte
		err  error
	}
	ch := make(chan answer, 1)
	go func() {
		line, err := c.out.ReadBytes('\n')
		ch <- answer{line, err}
	}()
	select {
	case ans := <-ch:
		if ans.err != nil {
			c.t.Fatalf("%s: the relay closed instead of answering: %v", method, ans.err)
		}
		var res map[string]any
		if err := json.Unmarshal(ans.line, &res); err != nil {
			c.t.Fatalf("%s: answered with non-JSON %q", method, ans.line)
		}
		return res
	case <-time.After(5 * time.Second):
		c.t.Fatalf("%s: no answer in 5s", method)
	}
	return nil
}

func toolText(t *testing.T, res map[string]any) (string, bool) {
	t.Helper()
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", res)
	}
	content := result["content"].([]any)[0].(map[string]any)
	return content["text"].(string), result["isError"] == true
}

func TestMCPStaysShutUntilBothSwitchesAreOn(t *testing.T) {
	a, _ := mcpApp(t, true)
	a.cfg.MCP.Enabled = false
	hosting(t, a)
	if mcpRunning(a) {
		t.Fatal("the socket opened with mcp.enabled off in the manifest")
	}
	a.closeMCP()

	a.cfg.MCP.Enabled = true
	setMCPUser(t, false)
	a.openMCP()
	if mcpRunning(a) {
		t.Fatal("the socket opened although the user never turned MCP on")
	}
	a.closeMCP()

	setMCPUser(t, true)
	a.openMCP()
	if !mcpRunning(a) {
		t.Fatal("both switches are on and the socket is still shut")
	}
	if _, err := os.Stat(filepath.Join(mcpDirOverride, mcpEndpointFile)); err != nil {
		t.Fatalf("no endpoint file for the relay to find: %v", err)
	}
}

func TestMCPRelayServesOnlyTheMarkedCommands(t *testing.T) {
	a, _ := mcpApp(t, true)
	hosting(t, a)
	c := relay(t, a.cfg)

	if res := c.call("initialize", map[string]any{"protocolVersion": "2025-06-18"}); res["result"] == nil {
		t.Fatalf("initialize: %v", res)
	}
	tools := c.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any)
	byName := map[string]map[string]any{}
	for _, raw := range tools {
		tool := raw.(map[string]any)
		byName[tool["name"].(string)] = tool
	}
	if len(byName) != 2 || byName["sum"] == nil || byName["wipe"] == nil {
		t.Fatalf("tools/list = %v, want exactly sum and wipe", tools)
	}
	// The spec defaults destructiveHint to true, so a harmless tool must say false.
	if hint := byName["sum"]["annotations"].(map[string]any)["destructiveHint"]; hint != false {
		t.Errorf("sum destructiveHint = %v, want false", hint)
	}
	if hint := byName["wipe"]["annotations"].(map[string]any)["destructiveHint"]; hint != true {
		t.Errorf("wipe destructiveHint = %v, want true", hint)
	}

	text, isErr := toolText(t, c.call("tools/call", map[string]any{"name": "sum", "arguments": map[string]any{"a": 20, "b": 22}}))
	if isErr || !strings.Contains(text, `"sum":42`) {
		t.Fatalf("sum(20, 22) = %q (error %v)", text, isErr)
	}
	// echo and count are registered commands without @mcp.
	for _, name := range []string{"echo", "count", "sys:mcp:enable"} {
		if res := c.call("tools/call", map[string]any{"name": name}); res["error"] == nil {
			t.Errorf("%s is not @mcp but was callable: %v", name, res)
		}
	}
}

func TestMCPRefusesAWrongToken(t *testing.T) {
	a, _ := mcpApp(t, true)
	hosting(t, a)
	var ep mcpEndpoint
	b, _ := os.ReadFile(filepath.Join(mcpDirOverride, mcpEndpointFile))
	if err := json.Unmarshal(b, &ep); err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"wrong": ep.Token + "x", "empty": ""} {
		conn, err := net.DialTimeout("unix", ep.Addr, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		hello, _ := json.Marshal(map[string]string{"token": token})
		_, _ = conn.Write(append(hello, '\n'))
		_, _ = conn.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"))
		all, _ := io.ReadAll(conn)
		_ = conn.Close()
		if strings.Contains(string(all), "tools") || !strings.Contains(string(all), "unauthorized") {
			t.Errorf("%s token: got %q", name, all)
		}
	}
}

func TestMCPDestructiveToolAsksTheUserFirst(t *testing.T) {
	a, wiped := mcpApp(t, true)
	hosting(t, a)
	allow := false
	var asked string
	old := mcpConfirm
	mcpConfirm = func(_ context.Context, _, text string) bool { asked = text; return allow }
	t.Cleanup(func() { mcpConfirm = old })
	c := relay(t, a.cfg)

	text, isErr := toolText(t, c.call("tools/call", map[string]any{"name": "wipe", "arguments": map[string]any{"scope": "all"}}))
	if !isErr || !strings.Contains(text, fault.CodeDenied) || wiped.Load() != 0 {
		t.Fatalf("a declined wipe answered %q (error %v) and ran %d times", text, isErr, wiped.Load())
	}
	if !strings.Contains(asked, "wipe") || !strings.Contains(asked, `"scope":"all"`) {
		t.Errorf("the dialog did not say what would run: %q", asked)
	}
	allow = true
	if _, isErr := toolText(t, c.call("tools/call", map[string]any{"name": "wipe"})); isErr || wiped.Load() != 1 {
		t.Fatalf("an allowed wipe failed or did not run (runs %d)", wiped.Load())
	}
	asked = ""
	toolText(t, c.call("tools/call", map[string]any{"name": "sum", "arguments": map[string]any{"a": 1, "b": 1}}))
	if asked != "" {
		t.Error("a tool that is not destructive raised the dialog")
	}

	log, err := os.ReadFile(filepath.Join(mcpDirOverride, mcpAuditFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"outcome":"denied"`) || !strings.Contains(lines[1], `"outcome":"ok"`) {
		t.Fatalf("audit log = %q, want denied, ok, ok", log)
	}
}

func (c *mcpClient) send(v any) {
	b, _ := json.Marshal(v)
	go func() { _, _ = c.in.Write(append(b, '\n')) }()
}

func (c *mcpClient) initialize(client string) {
	c.t.Helper()
	c.call("initialize", map[string]any{"protocolVersion": "2025-06-18", "clientInfo": map[string]any{"name": client}})
}

func TestMCPCallCarriesItsIdentity(t *testing.T) {
	a, _ := mcpApp(t, true)
	a.Command("whoami", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		call, ok := MCPCallFrom(ctx)
		_, hasClient := ClientFrom(ctx)
		return map[string]any{"mcp": ok, "tool": call.Tool, "client": call.Client, "path": call.ClientPath, "frontend": hasClient}, nil
	})
	a.MCPTool(MCPTool{Name: "whoami", Command: "whoami", Description: "Says who asked.", InputSchema: json.RawMessage(`{"type":"object"}`)})
	hosting(t, a)
	c := relay(t, a.cfg)
	c.initialize("claude-ai")
	text, _ := toolText(t, c.call("tools/call", map[string]any{"name": "whoami"}))
	want, _ := json.Marshal(map[string]any{"client": "claude-ai", "frontend": false, "mcp": true, "path": testClientPath, "tool": "whoami"})
	if text != string(want) {
		t.Errorf("through MCP the command saw %s", text)
	}
	direct, _ := a.reg.Invoke(context.Background(), "whoami", nil)
	if !strings.Contains(string(direct), `"mcp":false`) {
		t.Errorf("a call that did not come through MCP was marked as one: %s", direct)
	}
}

func TestMCPCallIsAnnouncedToTheFrontend(t *testing.T) {
	a, _ := mcpApp(t, true)
	got := make(chan webview.Message, 4)
	a.addSender(func(raw []byte) {
		var m webview.Message
		if json.Unmarshal(raw, &m) == nil && m.Name == "sys:mcp:call" {
			got <- m
		}
	})
	hosting(t, a)
	c := relay(t, a.cfg)
	c.initialize("claude-ai")
	c.call("tools/call", map[string]any{"name": "sum", "arguments": map[string]any{"a": 1, "b": 2}})
	select {
	case m := <-got:
		var ev map[string]any
		_ = json.Unmarshal(m.Data, &ev)
		if ev["tool"] != "sum" || ev["client"] != "claude-ai" || ev["client_path"] != testClientPath || ev["outcome"] != "ok" {
			t.Errorf("sys:mcp:call = %v", ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no sys:mcp:call reached the frontend")
	}
}

func TestMCPWarnsWhenSwitchedOnWithNothingToServe(t *testing.T) {
	mcpDirOverride = t.TempDir()
	t.Cleanup(func() { mcpDirOverride = "" })
	a := newTestApp(t)
	a.cfg.MCP.Enabled = true
	hosting(t, a)
	for _, line := range logger.Tail() {
		if strings.Contains(line, "no command is @mcp") {
			return
		}
	}
	t.Fatal("mcp.enabled with no @mcp command left no line in the log")
}

func TestMCPToolSwitchTurnsOneToolOff(t *testing.T) {
	a, _ := mcpApp(t, true)
	hosting(t, a)
	open := relay(t, a.cfg)
	open.initialize("claude-ai")

	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:tool", json.RawMessage(`{"name":"sum","enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if text, isErr := toolText(t, open.call("tools/call", map[string]any{"name": "sum", "arguments": map[string]any{"a": 1, "b": 1}})); !isErr || !strings.Contains(text, fault.CodeDenied) {
		t.Errorf("a switched-off tool still ran on a connection opened before: %s", text)
	}
	fresh := relay(t, a.cfg)
	fresh.initialize("claude-ai")
	for _, tool := range fresh.call("tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		if tool.(map[string]any)["name"] == "sum" {
			t.Error("a switched-off tool is still listed to a new connection")
		}
	}
	for _, on := range []string{`{"enabled":false}`, `{"enabled":true}`} {
		if _, err := a.reg.Invoke(context.Background(), "sys:mcp:enable", json.RawMessage(on)); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range a.mcpStatus().Tools {
		if tool.Name == "sum" && tool.Enabled {
			t.Error("turning MCP off and on again switched a tool back on")
		}
	}
	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:tool", json.RawMessage(`{"name":"nope","enabled":false}`)); fault.CodeOf(err) != fault.CodeNotFound {
		t.Errorf("switching an unknown tool = %v, want not_found", err)
	}
}

func TestMCPRedactsSecretsInTheDialogAndTheAudit(t *testing.T) {
	a, _ := mcpApp(t, true)
	hosting(t, a)
	var asked string
	old := mcpConfirm
	mcpConfirm = func(_ context.Context, _, text string) bool { asked = text; return false }
	t.Cleanup(func() { mcpConfirm = old })
	c := relay(t, a.cfg)
	c.initialize("claude-ai")
	c.call("tools/call", map[string]any{"name": "wipe", "arguments": map[string]any{
		"scope": "all", "password": "hunter2", "auth": map[string]any{"api_token": "abc123"}}})

	log, _ := os.ReadFile(filepath.Join(mcpDirOverride, mcpAuditFile))
	for where, text := range map[string]string{"dialog": asked, "audit": string(log)} {
		if strings.Contains(text, "hunter2") || strings.Contains(text, "abc123") {
			t.Errorf("the %s shows a secret: %s", where, text)
		}
		if !strings.Contains(text, "all") || !strings.Contains(text, "[redacted]") {
			t.Errorf("the %s lost what is not secret, or does not say what it hid: %s", where, text)
		}
	}
	if !strings.Contains(asked, "claude-ai") {
		t.Errorf("the dialog does not name the client that asked: %s", asked)
	}
}

func TestMCPProgressFromACommand(t *testing.T) {
	a, _ := mcpApp(t, true)
	a.Command("scan", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		return map[string]bool{"reported": MCPProgress(ctx, "halfway")}, nil
	})
	a.MCPTool(MCPTool{Name: "scan", Command: "scan", Description: "Scans.", InputSchema: json.RawMessage(`{"type":"object"}`)})
	hosting(t, a)
	c := relay(t, a.cfg)
	c.initialize("claude-ai")
	note := c.call("tools/call", map[string]any{"name": "scan", "_meta": map[string]any{"progressToken": "p1"}})
	if note["method"] != "notifications/progress" || note["params"].(map[string]any)["message"] != "halfway" {
		t.Fatalf("first message = %v, want the command's progress", note)
	}
	if MCPProgress(context.Background(), "nobody") {
		t.Error("MCPProgress outside an MCP call reported sending")
	}
}

func TestMCPNullReplyStillMeetsTheOutputSchema(t *testing.T) {
	a, _ := mcpApp(t, true)
	a.Command("nothing", func(context.Context, json.RawMessage, ipc.Stream) (any, error) { return nil, nil })
	a.MCPTool(MCPTool{Name: "nothing", Command: "nothing", Description: "Returns nothing.",
		InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{}}`)})
	hosting(t, a)
	c := relay(t, a.cfg)
	res := c.call("tools/call", map[string]any{"name": "nothing"})
	if result := res["result"].(map[string]any); result["structuredContent"] == nil {
		t.Errorf("a nil reply under an outputSchema = %v", result)
	}
}

func TestMCPToolTimeoutIsItsOwn(t *testing.T) {
	a, _ := mcpApp(t, true)
	a.Command("hang", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	a.MCPTool(MCPTool{Name: "hang", Command: "hang", Description: "Never ends.", InputSchema: json.RawMessage(`{"type":"object"}`), Timeout: 200 * time.Millisecond})
	hosting(t, a)
	c := relay(t, a.cfg)
	start := time.Now()
	if _, isErr := toolText(t, c.call("tools/call", map[string]any{"name": "hang"})); !isErr || time.Since(start) > 3*time.Second {
		t.Errorf("a 200ms tool answered error=%v after %s", isErr, time.Since(start))
	}
}

// Two enables at once could leave the endpoint naming a closed listener.
func TestMCPConcurrentEnablesLeaveOneWorkingEndpoint(t *testing.T) {
	for round := range 20 {
		a, _ := mcpApp(t, true)
		a.mu.Lock()
		a.mcpHost = true
		a.mu.Unlock()
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); _ = a.startMCP() }()
		}
		wg.Wait()
		conn, err := dialMCP("")
		if err != nil {
			a.closeMCP()
			t.Fatalf("round %d: the endpoint on disk does not reach the socket that is serving: %v", round, err)
		}
		_ = conn.Close()
		a.closeMCP()
	}
}

func TestMCPStoppingOneInstanceKeepsTheOtherReachable(t *testing.T) {
	first, _ := mcpApp(t, true)
	dir := mcpDirOverride
	second, _ := mcpApp(t, true)
	mcpDirOverride = dir
	hosting(t, first)
	hosting(t, second)
	first.closeMCP()
	conn, err := dialMCP("")
	if err != nil {
		t.Fatalf("the instance that quit deleted the endpoint of the one still serving: %v", err)
	}
	_ = conn.Close()
}

// The close-before-modules order only protects the database if it waits.
func TestMCPCloseWaitsForACallInFlight(t *testing.T) {
	a, _ := mcpApp(t, true)
	entered, release := make(chan struct{}), make(chan struct{})
	a.Command("slow", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		close(entered)
		<-release
		return "done", nil
	})
	a.MCPTool(MCPTool{Name: "slow", Command: "slow", Description: "Takes a while.", InputSchema: json.RawMessage(`{"type":"object"}`)})
	hosting(t, a)
	c := relay(t, a.cfg)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "slow"}})
	go func() { _, _ = c.in.Write(append(b, '\n')) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never reached the command")
	}
	closed := make(chan struct{})
	go func() { a.closeMCP(); close(closed) }()
	select {
	case <-closed:
		close(release)
		t.Fatal("closeMCP returned with a call still running, so the modules would stop under it")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closeMCP did not return after the call finished")
	}
}

func TestMCPHandshakeRefusesAnOverlongLineAtOnce(t *testing.T) {
	a, _ := mcpApp(t, true)
	hosting(t, a)
	var ep mcpEndpoint
	b, _ := os.ReadFile(filepath.Join(mcpDirOverride, mcpEndpointFile))
	if err := json.Unmarshal(b, &ep); err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("unix", ep.Addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	go func() { _, _ = conn.Write(bytes.Repeat([]byte("a"), 64<<10)) }()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	// Timed, not read: a reset on unread input can take the refusal text with it.
	_, _ = io.ReadAll(conn)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a token line with no end held the connection for %s", took)
	}
}

func TestClipNeverSplitsACharacter(t *testing.T) {
	s := []byte(strings.Repeat("é", 10))
	for n := 1; n < len(s); n++ {
		if got := clip(s, n); !utf8.ValidString(got) {
			t.Fatalf("clip at %d bytes = %q, which is not valid UTF-8", n, got)
		}
	}
}

// Found by the Windows E2E: the dialog outlived the client by the whole 60s grace.
func TestMCPConfirmationEndsWhenTheAgentLeaves(t *testing.T) {
	a, wiped := mcpApp(t, true)
	hosting(t, a)
	asked, closed := make(chan struct{}), make(chan struct{})
	old := mcpConfirm
	mcpConfirm = func(ctx context.Context, _, _ string) bool {
		close(asked)
		<-ctx.Done() // a user who never answers
		close(closed)
		return true // what a late Allow would say
	}
	t.Cleanup(func() { mcpConfirm = old })

	c := relay(t, a.cfg)
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "wipe"}})
	go func() { _, _ = c.in.Write(append(b, '\n')) }()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the dialog was never raised")
	}
	_ = c.in.Close()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the dialog stayed up after the agent that asked had gone")
	}
	time.Sleep(100 * time.Millisecond)
	if wiped.Load() != 0 {
		t.Fatal("an Allow that came after the agent left still ran the destructive command")
	}
}

func TestMCPSwitchStartsAndStopsTheSocket(t *testing.T) {
	a, _ := mcpApp(t, false)
	hosting(t, a)
	if mcpRunning(a) {
		t.Fatal("open before the user switched it on")
	}
	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:enable", json.RawMessage(`{"enabled":true}`)); err != nil {
		t.Fatal(err)
	}
	if !mcpRunning(a) || !mcpUserEnabled("") {
		t.Fatal("switching on neither opened the socket nor stuck")
	}
	c := relay(t, a.cfg)
	c.call("initialize", nil)

	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:enable", json.RawMessage(`{"enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	if mcpRunning(a) {
		t.Fatal("switching off left the socket open")
	}
	if _, err := os.Stat(filepath.Join(mcpDirOverride, mcpEndpointFile)); !os.IsNotExist(err) {
		t.Error("the endpoint file outlived the socket, so a relay would dial a dead port")
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("an open relay kept running after the user switched MCP off")
	}
}

func TestMCPClientCommandSurvivesAnAppImageMount(t *testing.T) {
	a, _ := mcpApp(t, false)
	t.Setenv("APPIMAGE", "/home/someone/Apps/Demo.AppImage")
	if got := a.mcpStatus().Client.Command; got != "/home/someone/Apps/Demo.AppImage" {
		t.Errorf("client command under an AppImage = %q, want the image itself", got)
	}
}

func TestMCPSwitchRefusesAnAppThatOffersNothing(t *testing.T) {
	a, _ := mcpApp(t, false)
	a.cfg.MCP.Enabled = false
	_, err := a.reg.Invoke(context.Background(), "sys:mcp:enable", json.RawMessage(`{"enabled":true}`))
	if fault.CodeOf(err) != fault.CodeDenied {
		t.Fatalf("enable on an app without mcp.enabled = %v, want denied", err)
	}
	if mcpUserEnabled("") {
		t.Error("the refused switch was still saved")
	}
}

func TestMCPRelayStartsTheAppWhenNoneIsRunning(t *testing.T) {
	a, _ := mcpApp(t, true)
	launched := 0
	old := mcpLaunch
	mcpLaunch = func() error {
		launched++
		go func() { time.Sleep(300 * time.Millisecond); hosting(t, a) }()
		return nil
	}
	t.Cleanup(func() { mcpLaunch = old })

	c := relay(t, a.cfg)
	if res := c.call("tools/list", nil); res["result"] == nil {
		t.Fatalf("tools/list through a relay that had to start the app: %v", res)
	}
	if launched != 1 {
		t.Errorf("launched %d times, want 1", launched)
	}
}

func TestMCPRelaySaysWhyItWillNotStart(t *testing.T) {
	a, _ := mcpApp(t, true)
	oldLaunch, oldWait := mcpLaunch, mcpLaunchWait
	mcpLaunch = func() error { return nil } // an app that never opens its socket
	mcpLaunchWait = 500 * time.Millisecond
	t.Cleanup(func() { mcpLaunch, mcpLaunchWait = oldLaunch, oldWait })

	cases := []struct {
		name string
		prep func()
		want string
	}{
		{"never opens", func() {}, "did not open"},
		{"user switch off", func() { setMCPUser(t, false) }, "turn it on"},
		{"manifest off", func() { a.cfg.MCP.Enabled = false }, "mcp.enabled is off"},
	}
	for _, tc := range cases {
		tc.prep()
		err := proxyMCP(context.Background(), a.cfg, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}
}

// stdout is the protocol stream, and the config load logs.
func TestRunMCPProxyKeepsStdoutForTheProtocol(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	// RunMCPProxy replaces the process-wide logger.
	t.Cleanup(func() { logger.New(logger.Config{Level: "info", Format: "console", Output: "stdout"}) })
	stdout := os.Stdout
	os.Stdout = w
	runErr := RunMCPProxy([]byte("app:\n  name: nothing-here\n"))
	os.Stdout = stdout
	_ = w.Close()
	got, _ := io.ReadAll(r)
	if runErr == nil {
		t.Fatal("a manifest without mcp.enabled started a relay")
	}
	if len(got) != 0 {
		t.Fatalf("the relay wrote to stdout before speaking MCP: %q", got)
	}
}

func TestMCPAsksBeforeANewProgramRunsAnything(t *testing.T) {
	a, ran := mcpApp(t, true)
	cursor := filepath.Join(string(filepath.Separator)+"apps", "cursor")
	connectAs(t, cursor)
	hosting(t, a)
	var asked []string
	answer := false
	old := mcpConfirm
	mcpConfirm = func(_ context.Context, _, text string) bool { asked = append(asked, text); return answer }
	t.Cleanup(func() { mcpConfirm = old })
	sum := map[string]any{"name": "sum", "arguments": map[string]any{"a": 20, "b": 22}}

	c := relay(t, a.cfg)
	c.initialize("cursor-ai")
	c.call("tools/list", nil)
	if len(asked) != 0 {
		t.Fatal("listing the tools asked the user; only running one should")
	}
	if text, isErr := toolText(t, c.call("tools/call", sum)); !isErr || !strings.Contains(text, fault.CodeDenied) {
		t.Fatalf("a program the user denied ran sum: %s", text)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], cursor) || !strings.Contains(asked[0], "cursor-ai") {
		t.Fatalf("the question did not name the program and what it calls itself: %q", asked)
	}
	if _, isErr := toolText(t, c.call("tools/call", map[string]any{"name": "wipe"})); !isErr || ran.Load() != 0 {
		t.Fatal("a denied program reached a destructive tool")
	}
	again := relay(t, a.cfg)
	toolText(t, again.call("tools/call", sum))
	if len(asked) != 1 {
		t.Fatalf("asked %d times after a Deny, want 1: a program that reconnects must not bring the dialog back", len(asked))
	}

	// Switching MCP off and on is how the user takes a Deny back.
	for _, on := range []string{`{"enabled":false}`, `{"enabled":true}`} {
		if _, err := a.reg.Invoke(context.Background(), "sys:mcp:enable", json.RawMessage(on)); err != nil {
			t.Fatal(err)
		}
	}
	answer = true
	later := relay(t, a.cfg)
	later.initialize("cursor-ai")
	if text, isErr := toolText(t, later.call("tools/call", sum)); isErr || !strings.Contains(text, `"sum":42`) {
		t.Fatalf("an allowed program could not run sum: %s", text)
	}
	next := relay(t, a.cfg)
	toolText(t, next.call("tools/call", sum))
	if len(asked) != 2 {
		t.Errorf("asked %d times, want 2: an allowed program was asked about again", len(asked))
	}
	var remembered *mcpApprovedClient
	for _, c := range a.mcpStatus().Clients {
		if samePath(c.Path, cursor) {
			remembered = &c
		}
	}
	if remembered == nil || remembered.Name != "cursor-ai" || remembered.ApprovedAt.IsZero() {
		t.Errorf("status clients = %+v, want cursor remembered with its name and when", a.mcpStatus().Clients)
	}
	log, _ := os.ReadFile(filepath.Join(mcpDirOverride, mcpAuditFile))
	first, _, _ := strings.Cut(string(log), "\n")
	var entry map[string]any
	if json.Unmarshal([]byte(first), &entry) != nil || entry["client_path"] != cursor || entry["outcome"] != "denied" {
		t.Errorf("the audit does not say which program was refused: %s", first)
	}
}

func TestMCPAsksOncePerProgramAtATime(t *testing.T) {
	a, _ := mcpApp(t, true)
	connectAs(t, filepath.Join(string(filepath.Separator)+"apps", "cursor"))
	hosting(t, a)
	var asks atomic.Int32
	release := make(chan struct{})
	old := mcpConfirm
	mcpConfirm = func(context.Context, string, string) bool { asks.Add(1); <-release; return true }
	t.Cleanup(func() { mcpConfirm = old })

	answers := make(chan string, 2)
	start := func() {
		c := relay(t, a.cfg)
		c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "sum", "arguments": map[string]any{"a": 1, "b": 1}}})
		go func() { line, _ := c.out.ReadBytes('\n'); answers <- string(line) }()
	}
	start()
	for deadline := time.Now().Add(5 * time.Second); asks.Load() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the first call never asked")
		}
	}
	start() // a second connection from the same program while the dialog is up
	time.Sleep(300 * time.Millisecond)
	if n := asks.Load(); n != 1 {
		close(release)
		t.Fatalf("one program opened %d dialogs at once", n)
	}
	close(release)
	for range 2 {
		select {
		case line := <-answers:
			if !strings.Contains(line, `\"sum\":2`) {
				t.Errorf("a call that waited on the same question = %s", line)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a call waiting on the question never finished")
		}
	}
}

func TestMCPQuestionNobodyAnsweredIsNotADenial(t *testing.T) {
	a, _ := mcpApp(t, true)
	connectAs(t, filepath.Join(string(filepath.Separator)+"apps", "cursor"))
	hosting(t, a)
	var asks atomic.Int32
	asked := make(chan struct{}, 2)
	old := mcpConfirm
	mcpConfirm = func(ctx context.Context, _, _ string) bool {
		if asks.Add(1) == 1 {
			asked <- struct{}{}
			<-ctx.Done()
			return false
		}
		return true
	}
	t.Cleanup(func() { mcpConfirm = old })

	c := relay(t, a.cfg)
	c.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "sum"}})
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("the call never asked")
	}
	_ = c.in.Close()
	go func() { _, _ = io.Copy(io.Discard, c.out) }() // a pipe nobody reads blocks the relay
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay did not end after its client left")
	}
	fresh := relay(t, a.cfg)
	if text, isErr := toolText(t, fresh.call("tools/call", map[string]any{"name": "sum", "arguments": map[string]any{"a": 1, "b": 1}})); isErr {
		t.Fatalf("a question nobody answered counted as a Deny: %s", text)
	}
	if asks.Load() != 2 {
		t.Errorf("asked %d times, want 2", asks.Load())
	}
}

func TestMCPRevokeEndsTheProgramsConnections(t *testing.T) {
	a, _ := mcpApp(t, true)
	hosting(t, a)
	c := relay(t, a.cfg)
	c.initialize("claude-ai")
	if _, isErr := toolText(t, c.call("tools/call", map[string]any{"name": "sum", "arguments": map[string]any{"a": 1, "b": 1}})); isErr {
		t.Fatal("an approved program could not run sum")
	}
	revoke, _ := json.Marshal(map[string]string{"path": testClientPath})
	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:revoke", revoke); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a revoked program kept its open connection")
	}
	if clients := a.mcpStatus().Clients; len(clients) != 0 {
		t.Errorf("status still lists %+v after the revoke", clients)
	}
	var asks atomic.Int32
	old := mcpConfirm
	mcpConfirm = func(context.Context, string, string) bool { asks.Add(1); return false }
	t.Cleanup(func() { mcpConfirm = old })
	fresh := relay(t, a.cfg)
	if _, isErr := toolText(t, fresh.call("tools/call", map[string]any{"name": "sum"})); !isErr || asks.Load() != 1 {
		t.Errorf("after a revoke the program ran without being asked (asked %d times)", asks.Load())
	}
	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:revoke", json.RawMessage(`{"path":"/nobody"}`)); fault.CodeOf(err) != fault.CodeNotFound {
		t.Errorf("revoking a program never allowed = %v, want not_found", err)
	}
}

func TestMCPRelayReportsARefusalInsteadOfStartingTheApp(t *testing.T) {
	a, _ := mcpApp(t, true)
	old := mcpIdentify
	mcpIdentify = func(net.Conn) (mcpPeer, error) {
		return mcpPeer{}, errors.New("the program that started the relay has already exited")
	}
	launched := 0
	oldLaunch := mcpLaunch
	mcpLaunch = func() error { launched++; return nil }
	t.Cleanup(func() { mcpIdentify, mcpLaunch = old, oldLaunch })
	hosting(t, a)

	err := proxyMCP(context.Background(), a.cfg, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already exited") {
		t.Errorf("relay error = %v, want the app's reason", err)
	}
	if launched != 0 {
		t.Errorf("the relay started the app %d times although it was running and had answered", launched)
	}
}
