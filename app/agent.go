package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tradalab/scorix/fault"
	"github.com/tradalab/scorix/internal/mcpcore"
	"github.com/tradalab/scorix/llm/agent"
)

// Named in the audit log and in MCPCallFrom, so a handler can tell a call the
// app's own agent made from one a person clicked.
const agentClient = "in-app agent"

// The @mcp rpcs, on the terms an MCP client gets them: a tool the user turned
// off in mcp.json is refused, each call has the tool's timeout, and each is
// audited and announced on sys:mcp:call. Whether MCP is served to outside
// clients does not matter here. Destructive calls are left to the loop's
// Confirm, which asks in the app's own UI.
func (a *App) AgentTools() []agent.Tool {
	a.mu.Lock()
	tools := append([]MCPTool(nil), a.mcpTools...)
	a.mu.Unlock()
	settings := loadMCPSettings(a.appDataName())
	out := make([]agent.Tool, 0, len(tools))
	for _, t := range tools {
		// Left off the list, not only refused when called: a model told about a
		// tool it cannot use spends a step finding that out.
		if settings.toolOff(t.Name) {
			continue
		}
		out = append(out, agent.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
			Destructive: t.Destructive,
			Run:         a.agentRun(t),
		})
	}
	return out
}

// A handler that panics comes back as an error, the way it does for an MCP
// client: the app's own assistant must not be the one caller that can take the
// process down with it.
func (a *App) invokeGuarded(ctx context.Context, command string, args json.RawMessage) (res json.RawMessage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return a.reg.Invoke(ctx, command, args)
}

// Records what the loop settled without reaching a command: a destructive call
// the user declined, one an app with no way to ask refused, an unknown tool.
// Apps pass it as agent.Options.OnEvent; without it the audit log says less
// about the app's own agent than about any MCP client.
func (a *App) AgentEvent(e agent.Event) {
	// Only what the loop settled by itself: a command that ran recorded itself,
	// and recording it again would double every failure in the log.
	if e.Kind != agent.ToolFinished || !e.Refused {
		return
	}
	call := MCPCall{Tool: e.Call.Name, Client: agentClient}
	a.mcpRecord(call, RedactArgs(json.RawMessage(e.Call.Arguments)), time.Now(), "denied", errors.New(e.Result))
}

// Hides the values of fields whose name reads as a credential, as the dialog
// and the audit log do for an MCP call. An app's Confirm shows the model's
// arguments to the user, and they are the model's to choose.
func RedactArgs(args json.RawMessage) json.RawMessage { return redactArgs(args) }

func (a *App) agentRun(t MCPTool) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		call := MCPCall{Tool: t.Name, Client: agentClient}
		shown := redactArgs(args)
		start := time.Now()
		if loadMCPSettings(a.appDataName()).toolOff(t.Name) {
			err := fault.Errorf(fault.CodeDenied, "the user turned %s off in the app", t.Name)
			a.mcpRecord(call, shown, start, "denied", err)
			return "", err
		}
		// The same check an MCP client's arguments go through: a field the tool
		// has no place for reaches the handler as a zero value otherwise, and
		// the user approved the arguments they were shown, not those.
		var given map[string]any
		if len(args) > 0 && json.Unmarshal(args, &given) != nil {
			err := errors.New("the arguments are not a JSON object")
			a.mcpRecord(call, shown, start, "error", err)
			return "", err
		}
		if err := mcpcore.CheckArgs(schemaMap(t.InputSchema), given); err != nil {
			a.mcpRecord(call, shown, start, "error", err)
			return "", err
		}
		limit := t.Timeout
		if limit <= 0 {
			limit = mcpcore.DefaultToolTimeout
		}
		ctx, cancel := context.WithTimeout(context.WithValue(ctx, mcpCallKey{}, call), limit)
		defer cancel()
		res, err := a.invokeGuarded(ctx, t.Command, args)
		if err != nil {
			a.mcpRecord(call, shown, start, "error", err)
			return "", err
		}
		a.mcpRecord(call, shown, start, "ok", nil)
		if len(res) == 0 {
			return "null", nil
		}
		return string(res), nil
	}
}
