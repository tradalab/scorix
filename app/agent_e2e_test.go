package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tradalab/scorix/internal/ipc"
	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/agent"
	"github.com/tradalab/scorix/llm/ollama"
)

// A real model choosing among the app's own commands, which no scripted reply
// can stand in for: whether it calls the tool, with what, and what it makes of
// a refusal. Gated on SCORIX_AGENT_E2E_MODEL, an Ollama model that calls tools
// (smollm2:1.7b does, through Ollama's template); SCORIX_AGENT_E2E_URL
// defaults to Ollama's own address.
func TestAgentAgainstOllama(t *testing.T) {
	model := os.Getenv("SCORIX_AGENT_E2E_MODEL")
	if model == "" {
		t.Skip("set SCORIX_AGENT_E2E_MODEL to run")
	}
	a, _ := mcpApp(t, false)
	// wipe takes no arguments, and a 1.7B model answers that it lacks them
	// rather than call it; a destructive tool with one clear argument is what
	// a small model does call.
	var deleted atomic.Int32
	a.Command("delete_file", func(context.Context, json.RawMessage, ipc.Stream) (any, error) {
		deleted.Add(1)
		return map[string]bool{"deleted": true}, nil
	})
	a.MCPTool(MCPTool{Name: "delete_file", Command: "delete_file", Description: "Deletes the file at path.", Destructive: true,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"The file to delete"}},"required":["path"]}`)})
	p, err := ollama.New(ollama.Config{ID: "ollama", BaseURL: os.Getenv("SCORIX_AGENT_E2E_URL"), Capabilities: []llm.Capability{llm.Chat, llm.Tools}})
	if err != nil {
		t.Fatal(err)
	}
	reg := llm.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind("assistant", llm.Binding{Provider: "ollama", Model: model}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var events []string
	run := func(q string, confirm func(context.Context, llm.ToolCall) (bool, error)) *agent.Result {
		t.Helper()
		res, err := agent.Run(ctx, agent.Options{
			Registry: reg, Slot: "assistant", Tools: a.AgentTools(), MaxSteps: 4, Confirm: confirm,
			OnEvent: func(e agent.Event) {
				events = append(events, string(e.Kind)+" "+e.Call.Name+" "+e.Call.Arguments+" -> "+e.Result)
			},
		}, llm.ChatRequest{Messages: []llm.Message{
			{Role: llm.System, Content: "You are a helpful assistant. Use the tools for arithmetic and for deleting files."},
			{Role: llm.User, Content: q},
		}})
		// A small model that keeps retrying a refused tool runs out of steps,
		// which is the loop working, not the loop failing.
		if err != nil && !errors.Is(err, agent.ErrStepLimit) {
			t.Fatalf("%s: %v", q, err)
		}
		t.Logf("%q: %d steps, %d dropped, %+v, %v, answer %q", q, res.Steps, res.Dropped, res.Usage, err, res.Content)
		return res
	}

	// What the model then says is its own business: smollm2 answered "42" on
	// one run and "could not be answered" on the next, from the same result.
	// What the loop owes is that the result reached it.
	sum := run("What is 40 plus 2? Call the sum tool with a=40 and b=2.", nil)
	t.Logf("events %q", events)
	handed := false
	for _, m := range sum.Messages {
		handed = handed || (m.Role == llm.ToolResult && m.Content == `{"sum":42}` && !m.Failed)
	}
	if !handed {
		t.Errorf("the model was never handed the sum: %+v", sum.Messages)
	}

	events = nil
	var asked []string
	run("Delete the file notes.txt. Call the delete_file tool with path notes.txt.", func(_ context.Context, c llm.ToolCall) (bool, error) {
		asked = append(asked, c.Name+" "+c.Arguments)
		return false, nil
	})
	t.Logf("events %q, asked %q", events, asked)
	if deleted.Load() != 0 {
		t.Error("a declined delete ran")
	}
	if len(asked) == 0 || !strings.Contains(strings.Join(events, "\n"), "did not allow delete_file") {
		t.Errorf("the model never asked to delete, so the refusal was not exercised: %q", events)
	}
	for _, line := range auditLines(t) {
		t.Logf("audit %v", line)
	}
}
