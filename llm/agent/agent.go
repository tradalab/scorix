// Package agent runs the loop a model with tools needs: ask, run the tools it
// calls, hand back what they returned, ask again, until it answers. The app
// supplies the tools; scorix/app offers its @mcp rpcs as a ready set.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tradalab/scorix/llm"
)

// Returned with a Result when the model was still calling tools at the last
// step; the Result's Messages continue the run.
var ErrStepLimit = errors.New("agent: step limit reached while the model was still calling tools")

type Tool struct {
	Name        string
	Description string
	// JSON Schema of the arguments object.
	Parameters json.RawMessage
	// Every call waits for Options.Confirm.
	Destructive bool
	// What it returns goes back to the model as the result. An error does too,
	// marked as failed, so the model can correct the call.
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

type Options struct {
	Registry *llm.Registry
	Slot     string
	Tools    []Tool
	// Asked before each call to a Destructive tool, typically in the app's own
	// UI, and not again in the same run for a tool the user declined. Nil
	// refuses them all: a tool that deletes must not run because an app forgot
	// to wire a question.
	Confirm func(ctx context.Context, call llm.ToolCall) (bool, error)
	// Model requests in one run; zero is 10.
	MaxSteps int
	// Input tokens the model reads. Zero asks the provider, and a provider
	// that cannot say leaves the conversation untrimmed. Ollama reports the
	// model's trained length, which its server may not have loaded.
	Window int
	// Kept free for the reply; zero takes the request's MaxTokens, or an
	// eighth of the window.
	Reserve int
	OnChunk func(llm.Chunk) error
	OnEvent func(Event)
}

type EventKind string

const (
	ToolStarted  EventKind = "tool_started"
	ToolFinished EventKind = "tool_finished"
)

type Event struct {
	Kind EventKind
	Call llm.ToolCall
	// On ToolFinished: what went back to the model.
	Result string
	Failed bool
	// The loop settled it and the tool never ran: no tool of the app's
	// recorded it, so this event is the only record there is.
	Refused bool
}

type Result struct {
	// The whole conversation: what was passed in, then every turn the run
	// added. Trimming to the window applies to what is sent, never to this.
	Messages []llm.Message
	// The model's last answer.
	Content      string
	FinishReason string
	// Summed over every step.
	Usage llm.Usage
	Steps int
	// Messages the window forced out of the most recent request.
	Dropped int
}

// Tools go in req; req.Tools must be empty. A Result comes back with the
// error whenever there is one, so an app can show and keep what was done.
func Run(ctx context.Context, opt Options, req llm.ChatRequest) (*Result, error) {
	if len(req.Tools) > 0 {
		return nil, errors.New("agent: tools go in Options.Tools, where they can be run")
	}
	byName := map[string]Tool{}
	for _, t := range opt.Tools {
		if _, dup := byName[t.Name]; dup {
			return nil, fmt.Errorf("agent: two tools named %q", t.Name)
		}
		byName[t.Name] = t
		req.Tools = append(req.Tools, llm.Tool{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	steps := opt.MaxSteps
	if steps <= 0 {
		steps = 10
	}
	res := &Result{Messages: append([]llm.Message(nil), req.Messages...)}
	window, reserve, p, err := budget(ctx, opt, req)
	if err != nil {
		return res, err
	}
	declined := map[string]bool{}
	for res.Steps < steps {
		send := req
		send.Messages = res.Messages
		if window > 0 {
			fitted, err := llm.Fit(ctx, p, send, window, reserve)
			if err != nil {
				return res, err
			}
			send, res.Dropped = fitted.Request, fitted.Dropped
		}
		out, err := opt.Registry.Chat(ctx, opt.Slot, send, opt.OnChunk)
		res.Steps++
		if err != nil {
			return res, err
		}
		res.Usage = res.Usage.Add(out.Usage)
		res.Content, res.FinishReason = out.Content, out.FinishReason
		res.Messages = append(res.Messages, out.Message())
		if len(out.ToolCalls) == 0 {
			return res, nil
		}
		for i, call := range out.ToolCalls {
			o, err := runTool(ctx, opt, byName, declined, call)
			if err != nil {
				// Every call in the turn needs an answer, or the next request
				// carrying this conversation is refused. What a tool managed to
				// return before the stop is still what happened.
				if o.text != "" {
					res.Messages = append(res.Messages, llm.Message{Role: llm.ToolResult, ToolCallID: call.ID, Content: o.text, Failed: o.failed})
					i++
				}
				for _, left := range out.ToolCalls[i:] {
					res.Messages = append(res.Messages, llm.Message{Role: llm.ToolResult, ToolCallID: left.ID, Content: "the run stopped before this ran: " + err.Error(), Failed: true})
				}
				return res, err
			}
			res.Messages = append(res.Messages, llm.Message{Role: llm.ToolResult, ToolCallID: call.ID, Content: o.text, Failed: o.failed})
		}
	}
	return res, ErrStepLimit
}

func budget(ctx context.Context, opt Options, req llm.ChatRequest) (window, reserve int, p llm.Provider, err error) {
	if opt.Registry == nil {
		return 0, 0, nil, errors.New("agent: no registry")
	}
	b, ok := opt.Registry.Binding(opt.Slot)
	if !ok {
		return 0, 0, nil, fmt.Errorf("agent: slot %q: %w", opt.Slot, llm.ErrNoBinding)
	}
	p, ok = opt.Registry.Provider(b.Provider)
	if !ok {
		return 0, 0, nil, fmt.Errorf("agent: slot %q: provider %q: %w", opt.Slot, b.Provider, llm.ErrUnknownProvider)
	}
	window = opt.Window
	if window == 0 {
		model := req.Model
		if model == "" {
			model = b.Model
		}
		if window, err = llm.ContextWindow(ctx, p, model); err != nil {
			return 0, 0, nil, fmt.Errorf("agent: context window of %s: %w", model, err)
		}
	}
	reserve = opt.Reserve
	if reserve == 0 {
		reserve = req.MaxTokens
	}
	if reserve == 0 {
		reserve = window / 8
	}
	return window, reserve, p, nil
}

// What a call came to. Refused means the loop answered it by itself, with
// the tool untouched.
type outcome struct {
	text            string
	failed, refused bool
}

// An error back means the run stops: the context ended, or Confirm could not
// ask. Anything the model can act on is a failed result instead.
func runTool(ctx context.Context, opt Options, byName map[string]Tool, declined map[string]bool, call llm.ToolCall) (outcome, error) {
	emit := func(e Event) {
		if opt.OnEvent != nil {
			opt.OnEvent(e)
		}
	}
	emit(Event{Kind: ToolStarted, Call: call})
	o, err := invoke(ctx, opt, byName, declined, call)
	if err != nil {
		// Reported finished even so: a UI that opened a row for it has to close
		// it, and the run is about to end.
		emit(Event{Kind: ToolFinished, Call: call, Result: err.Error(), Failed: true, Refused: o.refused})
		return o, err
	}
	emit(Event{Kind: ToolFinished, Call: call, Result: o.text, Failed: o.failed, Refused: o.refused})
	return o, nil
}

func invoke(ctx context.Context, opt Options, byName map[string]Tool, declined map[string]bool, call llm.ToolCall) (outcome, error) {
	refuse := func(format string, a ...any) (outcome, error) {
		return outcome{text: fmt.Sprintf(format, a...), failed: true, refused: true}, nil
	}
	t, ok := byName[call.Name]
	if !ok {
		return refuse("there is no tool named %q", call.Name)
	}
	args := json.RawMessage(call.Arguments)
	if call.Arguments == "" {
		args = json.RawMessage("{}")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
		return refuse("the arguments are not a JSON object: %s", call.Arguments)
	}
	if t.Destructive {
		if opt.Confirm == nil {
			return refuse("%s changes data and this app does not allow it without asking the user", t.Name)
		}
		// A model refused once tends to try again, and each try would put the
		// same question to the user who just answered it.
		if declined[t.Name] {
			return refuse("the user already declined %s in this conversation; do not call it again", t.Name)
		}
		ok, err := opt.Confirm(ctx, call)
		if err != nil {
			return outcome{refused: true}, fmt.Errorf("agent: confirm %s: %w", t.Name, err)
		}
		if !ok {
			declined[t.Name] = true
			return refuse("the user did not allow %s", t.Name)
		}
	}
	out, err := t.Run(ctx, args)
	if err != nil {
		o := outcome{text: err.Error(), failed: true}
		if ctx.Err() != nil {
			return o, ctx.Err()
		}
		return o, nil
	}
	// Stopped, but the tool already ran: its answer is kept and the run ends.
	if ctx.Err() != nil {
		return outcome{text: out}, ctx.Err()
	}
	return outcome{text: out}, nil
}
