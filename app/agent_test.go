package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/agent"
	"github.com/tradalab/scorix/llm/llmtest"
	"github.com/tradalab/scorix/webview"
)

func agentTool(t *testing.T, a *App, name string) agent.Tool {
	t.Helper()
	for _, tool := range a.AgentTools() {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("no agent tool %q", name)
	return agent.Tool{}
}

func auditLines(t *testing.T) []map[string]any {
	t.Helper()
	raw, _ := os.ReadFile(filepath.Join(mcpDirOverride, mcpAuditFile))
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

// Offered whether or not MCP is served to outside clients: that switch is
// about programs on the machine, and this agent is the app's own.
func TestTheAgentGetsTheAppsMCPTools(t *testing.T) {
	a, wiped := mcpApp(t, false)
	a.cfg.MCP.Enabled = false
	sum, wipe := agentTool(t, a, "sum"), agentTool(t, a, "wipe")
	if sum.Destructive || !wipe.Destructive || string(sum.Parameters) != sumSchema || sum.Description != "Adds two numbers." {
		t.Errorf("sum %+v, wipe destructive %v", sum, wipe.Destructive)
	}
	out, err := sum.Run(context.Background(), json.RawMessage(`{"a":1,"b":2}`))
	if err != nil || out != `{"sum":3}` {
		t.Errorf("%s, %v", out, err)
	}
	// Asking is the loop's job, through Confirm; the tool itself just runs.
	if _, err := wipe.Run(context.Background(), json.RawMessage(`{}`)); err != nil || wiped.Load() != 1 {
		t.Errorf("%v, wiped %d", err, wiped.Load())
	}
	lines := auditLines(t)
	if len(lines) != 2 || lines[0]["tool"] != "sum" || lines[0]["client"] != agentClient || lines[0]["outcome"] != "ok" || lines[0]["client_path"] != "" {
		t.Errorf("audit %v", lines)
	}
}

func TestAnAgentCallSaysWhoMadeIt(t *testing.T) {
	a, _ := mcpApp(t, false)
	a.Command("whoami", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		call, ok := MCPCallFrom(ctx)
		_, hasClient := ClientFrom(ctx)
		return map[string]any{"mcp": ok, "tool": call.Tool, "client": call.Client, "path": call.ClientPath, "frontend": hasClient}, nil
	})
	a.MCPTool(MCPTool{Name: "whoami", Command: "whoami", InputSchema: json.RawMessage(`{"type":"object"}`)})
	out, err := agentTool(t, a, "whoami").Run(context.Background(), json.RawMessage(`{}`))
	want, _ := json.Marshal(map[string]any{"client": agentClient, "frontend": false, "mcp": true, "path": "", "tool": "whoami"})
	if err != nil || out != string(want) {
		t.Errorf("%s, %v", out, err)
	}
}

func TestAToolTheUserTurnedOffIsRefusedToTheAgentToo(t *testing.T) {
	a, wiped := mcpApp(t, false)
	wipe := agentTool(t, a, "wipe")
	if _, err := a.reg.Invoke(context.Background(), "sys:mcp:tool", json.RawMessage(`{"name":"wipe","enabled":false}`)); err != nil {
		t.Fatal(err)
	}
	// Off the list as well as refused: a tool the model is told about and then
	// denied costs it a step, and the refusal reads as the app misbehaving.
	for _, tool := range a.AgentTools() {
		if tool.Name == "wipe" {
			t.Error("a switched-off tool is still offered to the model")
		}
	}
	// The switch can move between listing and calling, so the call checks too.
	_, err := wipe.Run(context.Background(), json.RawMessage(`{}`))
	if fault.CodeOf(err) != fault.CodeDenied || wiped.Load() != 0 {
		t.Errorf("%v, wiped %d", err, wiped.Load())
	}
	if lines := auditLines(t); len(lines) != 1 || lines[0]["outcome"] != "denied" {
		t.Errorf("audit %v", lines)
	}
}

// The settings file is the record of what the user allowed and switched off.
// Unreadable, it used to read as "nothing is switched off", which turns every
// tool the user disabled back on, for MCP clients and for the app's own agent,
// with nothing in the interface to say so.
func TestSettingsThatCannotBeReadTurnNothingBackOn(t *testing.T) {
	a, wiped := mcpApp(t, true)
	// Taken while the file still reads, the way a run already under way holds
	// one: the call has to be refused too, not only the listing.
	sum := agentTool(t, a, "sum")
	path := filepath.Join(mcpDirOverride, mcpSettingsFile)
	if err := os.WriteFile(path, []byte(`{"enabled":true,"disabled_tools":["wi`), 0o600); err != nil {
		t.Fatal(err)
	}
	if tools := a.AgentTools(); len(tools) != 0 {
		t.Errorf("%d tools offered to the model", len(tools))
	}
	if n := len(a.mcpcoreTools(&mcpSession{})); n != 0 {
		t.Errorf("%d tools served to an MCP client", n)
	}
	for _, tool := range a.mcpStatus().Tools {
		if tool.Enabled {
			t.Errorf("the interface shows %s as on", tool.Name)
		}
	}
	// Writing would replace what could not be read with defaults, and the
	// switches would be gone for good.
	if err := a.updateMCPSettings(func(s *mcpSettings) { s.Enabled = false }); err == nil {
		t.Error("the unreadable file was overwritten")
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), `["wi`) {
		t.Errorf("the file changed: %s", b)
	}
	if _, err := sum.Run(context.Background(), json.RawMessage(`{"a":1,"b":1}`)); err == nil {
		t.Error("a tool ran")
	}
	if wiped.Load() != 0 {
		t.Error("the destructive command ran")
	}
}

// Written beside the file and moved over it, so a write that dies part way
// leaves the old file whole rather than a truncated one that reads as empty.
func TestSettingsAreWrittenWholeOrNotAtAll(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := writeJSON0600(path, mcpSettings{Enabled: true, DisabledTools: []string{"wipe"}}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON0600(path, func() {}); err == nil {
		t.Error("a value that cannot be written was reported as written")
	}
	// A move that cannot happen, because the name is a directory: the file
	// beside it does not stay behind.
	if err := os.Mkdir(filepath.Join(dir, "taken"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON0600(filepath.Join(dir, "taken"), mcpSettings{}); err == nil {
		t.Error("writing over a directory was reported as written")
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 2 {
		names := []string{}
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("%v: a half-written file was left behind", names)
	}
	b, _ := os.ReadFile(path)
	var s mcpSettings
	if json.Unmarshal(b, &s) != nil || !s.Enabled || len(s.DisabledTools) != 1 {
		t.Errorf("the file that was there is gone or broken: %s", b)
	}
}

func TestAgentCallsAreAnnouncedToTheFrontend(t *testing.T) {
	a, _ := mcpApp(t, false)
	a.Command("broken", func(context.Context, json.RawMessage, ipc.Stream) (any, error) { return nil, errors.New("disk full") })
	a.MCPTool(MCPTool{Name: "broken", Command: "broken", InputSchema: json.RawMessage(`{"type":"object"}`)})
	got := make(chan map[string]any, 4)
	a.addSender(func(raw []byte) {
		var m webview.Message
		if json.Unmarshal(raw, &m) == nil && m.Name == "sys:mcp:call" {
			var ev map[string]any
			_ = json.Unmarshal(m.Data, &ev)
			got <- ev
		}
	})
	if _, err := agentTool(t, a, "broken").Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("%v", err)
	}
	select {
	case ev := <-got:
		if ev["tool"] != "broken" || ev["client"] != agentClient || ev["outcome"] != "error" || !strings.Contains(ev["error"].(string), "disk full") {
			t.Errorf("sys:mcp:call = %v", ev)
		}
	case <-time.After(replyWait):
		t.Fatal("no sys:mcp:call reached the frontend")
	}
}

func TestAnAgentToolKeepsItsTimeout(t *testing.T) {
	a, _ := mcpApp(t, false)
	a.Command("hang", func(ctx context.Context, _ json.RawMessage, _ ipc.Stream) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	a.MCPTool(MCPTool{Name: "hang", Command: "hang", InputSchema: json.RawMessage(`{"type":"object"}`), Timeout: 200 * time.Millisecond})
	start := time.Now()
	if _, err := agentTool(t, a, "hang").Run(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Errorf("%v after %s", err, time.Since(start))
	}
}

// An MCP client gets a handler's panic as an error; the app's own agent used
// to get the process dying with it.
func TestAToolThatPanicsIsAnErrorNotADeadApp(t *testing.T) {
	a, _ := mcpApp(t, false)
	a.Command("boom", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		var m map[string]int
		m["x"] = 1
		return nil, nil
	})
	a.MCPTool(MCPTool{Name: "boom", Command: "boom", InputSchema: json.RawMessage(`{"type":"object"}`)})
	_, err := agentTool(t, a, "boom").Run(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "panic") {
		t.Fatalf("%v", err)
	}
	if lines := auditLines(t); len(lines) != 1 || lines[0]["outcome"] != "error" {
		t.Errorf("audit %v", lines)
	}
}

// The same check an MCP client's arguments go through: a misspelt field would
// otherwise reach the handler as a zero value, with the dialog showing the
// argument the user thought they were approving.
func TestTheModelsArgumentsAreCheckedAgainstTheSchema(t *testing.T) {
	a, _ := mcpApp(t, false)
	var ran atomic.Int32
	a.Command("counted", func(_ context.Context, data json.RawMessage, _ ipc.Stream) (any, error) {
		ran.Add(1)
		return map[string]int{"ok": 1}, nil
	})
	// Closed, the way `generate proto` writes an @mcp schema.
	closed := `{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"additionalProperties":false}`
	a.MCPTool(MCPTool{Name: "counted", Command: "counted", InputSchema: json.RawMessage(closed)})
	tool := agentTool(t, a, "counted")
	for name, args := range map[string]string{
		"a field the tool has no place for": `{"a":1,"paht":2}`,
		"a field of the wrong type":         `{"a":"one"}`,
	} {
		if _, err := tool.Run(context.Background(), json.RawMessage(args)); err == nil {
			t.Errorf("%s: ran", name)
		}
	}
	if ran.Load() != 0 {
		t.Errorf("the command ran %d times", ran.Load())
	}
	// An app can call a tool itself, without the loop's own check in front.
	if _, err := tool.Run(context.Background(), json.RawMessage(`[1]`)); err == nil {
		t.Error("arguments that are not an object ran")
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"a":1,"b":2}`)); err != nil || ran.Load() != 1 {
		t.Errorf("%v after %d runs", err, ran.Load())
	}
}

// What the loop refuses never reaches the command, so the app has to record it
// itself or the audit log understates what the model asked for.
func TestARefusedCallIsAuditedAndAnnounced(t *testing.T) {
	a, _ := mcpApp(t, false)
	got := make(chan map[string]any, 4)
	a.addSender(func(raw []byte) {
		var m webview.Message
		if json.Unmarshal(raw, &m) == nil && m.Name == "sys:mcp:call" {
			var ev map[string]any
			_ = json.Unmarshal(m.Data, &ev)
			got <- ev
		}
	})
	a.AgentEvent(agent.Event{Kind: agent.ToolFinished, Failed: true, Refused: true, Result: "the user did not allow wipe",
		Call: llm.ToolCall{Name: "wipe", Arguments: `{"scope":"all","password":"hunter2"}`}})
	// A tool the app does not have: the loop refused it, so the loop's word is
	// the only record there will be.
	a.AgentEvent(agent.Event{Kind: agent.ToolFinished, Failed: true, Refused: true, Result: `there is no tool named "search"`,
		Call: llm.ToolCall{Name: "search"}})
	// Not the loop's doing, so not the loop's to record: a start, an answer,
	// and a command that failed after recording itself.
	a.AgentEvent(agent.Event{Kind: agent.ToolStarted, Refused: true, Call: llm.ToolCall{Name: "wipe"}})
	a.AgentEvent(agent.Event{Kind: agent.ToolStarted, Call: llm.ToolCall{Name: "search"}})
	a.AgentEvent(agent.Event{Kind: agent.ToolFinished, Call: llm.ToolCall{Name: "wipe"}, Result: "done"})
	a.AgentEvent(agent.Event{Kind: agent.ToolFinished, Failed: true, Call: llm.ToolCall{Name: "wipe"}, Result: "disk full"})

	lines := auditLines(t)
	if len(lines) != 2 || lines[1]["tool"] != "search" {
		t.Fatalf("audit %v: only what the loop settled by itself belongs here", lines)
	}
	line := lines[0]
	args, _ := line["args"].(string)
	if line["tool"] != "wipe" || line["client"] != agentClient || line["outcome"] != "denied" ||
		!strings.Contains(line["error"].(string), "did not allow") {
		t.Errorf("audit %v", line)
	}
	if strings.Contains(args, "hunter2") || !strings.Contains(args, "[redacted]") || !strings.Contains(args, "all") {
		t.Errorf("the audit shows a secret: %s", args)
	}
	select {
	case ev := <-got:
		if ev["tool"] != "wipe" || ev["outcome"] != "denied" {
			t.Errorf("sys:mcp:call = %v", ev)
		}
	case <-time.After(replyWait):
		t.Fatal("no sys:mcp:call reached the frontend")
	}
}

func TestANullReplyReachesTheModelAsNull(t *testing.T) {
	a, _ := mcpApp(t, false)
	a.Command("nothing", func(context.Context, json.RawMessage, ipc.Stream) (any, error) { return nil, nil })
	a.MCPTool(MCPTool{Name: "nothing", Command: "nothing", InputSchema: json.RawMessage(`{"type":"object"}`)})
	if out, err := agentTool(t, a, "nothing").Run(context.Background(), json.RawMessage(`{}`)); err != nil || out != "null" {
		t.Errorf("%q, %v", out, err)
	}
}

// The whole path in one process: the model asks for an @mcp rpc, the app runs
// it, and the model reads the answer.
func TestAnAgentRunsTheAppsCommands(t *testing.T) {
	a, _ := mcpApp(t, false)
	p := llmtest.New("fake", llm.Chat, llm.Tools).Reply(
		llmtest.Reply{ToolCalls: []llm.ToolCall{{ID: "1", Name: "sum", Arguments: `{"a":40,"b":2}`}}},
		llmtest.Reply{Text: "42"},
	)
	reg := llm.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind("assistant", llm.Binding{Provider: "fake", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	res, err := agent.Run(context.Background(), agent.Options{Registry: reg, Slot: "assistant", Tools: a.AgentTools()},
		llm.ChatRequest{Messages: []llm.Message{{Role: llm.User, Content: "40 + 2?"}}})
	if err != nil || res.Content != "42" || res.Messages[2].Content != `{"sum":42}` {
		t.Errorf("%v, %+v", err, res)
	}
}
