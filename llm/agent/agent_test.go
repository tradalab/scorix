package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/tradalab/scorix/llm"
	"github.com/tradalab/scorix/llm/llmtest"
)

func rig(t *testing.T, p llm.Provider) *llm.Registry {
	t.Helper()
	reg := llm.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := reg.Bind("agent", llm.Binding{Provider: p.ID(), Model: "m"}); err != nil {
		t.Fatal(err)
	}
	return reg
}

func model() *llmtest.Provider { return llmtest.New("fake", llm.Chat, llm.Tools) }

func ask(q string) llm.ChatRequest {
	return llm.ChatRequest{Messages: []llm.Message{{Role: llm.System, Content: "Be brief."}, {Role: llm.User, Content: q}}}
}

type ran struct{ args []string }

func (r *ran) tool(name, out string, destructive bool) Tool {
	return Tool{Name: name, Description: "does " + name, Parameters: json.RawMessage(`{"type":"object"}`), Destructive: destructive,
		Run: func(_ context.Context, args json.RawMessage) (string, error) {
			r.args = append(r.args, name+" "+string(args))
			return out, nil
		}}
}

func calling(id, name, args string) llmtest.Reply {
	return llmtest.Reply{ToolCalls: []llm.ToolCall{{ID: id, Name: name, Arguments: args}}, Usage: llm.Usage{PromptTokens: 10, CompletionTokens: 2}}
}

func TestToolsRunUntilTheModelAnswers(t *testing.T) {
	signed := &llm.Replay{Driver: "anthropic", Data: json.RawMessage(`[{"type":"thinking"}]`)}
	first := calling("c1", "local_time", `{"city":"Tokyo"}`)
	first.Replay = signed
	p := model().Reply(first, llmtest.Reply{Text: "It is 10:42.", Usage: llm.Usage{PromptTokens: 30, CompletionTokens: 5}})
	r := &ran{}
	var events []string
	var chunks []string
	res, err := Run(context.Background(), Options{
		Registry: rig(t, p), Slot: "agent", Tools: []Tool{r.tool("local_time", "10:42 JST", false)},
		OnEvent: func(e Event) { events = append(events, string(e.Kind)+" "+e.Call.Name+" "+e.Result) },
		OnChunk: func(c llm.Chunk) error { chunks = append(chunks, string(c.Kind)); return nil },
	}, ask("Time in Tokyo?"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "It is 10:42." || res.Steps != 2 || res.Usage.PromptTokens != 40 || res.Usage.CompletionTokens != 7 || res.FinishReason != "stop" {
		t.Errorf("result %+v", res)
	}
	if strings.Join(r.args, "|") != `local_time {"city":"Tokyo"}` {
		t.Errorf("ran %v", r.args)
	}
	if strings.Join(events, "|") != "tool_started local_time |tool_finished local_time 10:42 JST" {
		t.Errorf("events %q", events)
	}
	if strings.Join(chunks, " ") != "tool_call text text text" {
		t.Errorf("chunks %q", chunks)
	}
	msgs := res.Messages
	if len(msgs) != 5 || msgs[2].Role != llm.Assistant || msgs[2].Replay != signed || len(msgs[2].ToolCalls) != 1 ||
		msgs[3].Role != llm.ToolResult || msgs[3].ToolCallID != "c1" || msgs[3].Content != "10:42 JST" || msgs[3].Failed || msgs[4].Content != "It is 10:42." {
		t.Fatalf("conversation %+v", msgs)
	}
	reqs := p.Requests()
	if len(reqs) != 2 || len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "local_time" || len(reqs[1].Messages) != 4 || reqs[1].Messages[2].Replay != signed {
		t.Errorf("requests %+v", reqs)
	}
}

func TestWhatTheModelGotWrongGoesBackAsAFailedResult(t *testing.T) {
	p := model().Reply(llmtest.Reply{ToolCalls: []llm.ToolCall{
		{ID: "a", Name: "nope", Arguments: "{}"},
		{ID: "b", Name: "look", Arguments: "[1]"},
		{ID: "c", Name: "look", Arguments: "{broken"},
		{ID: "d", Name: "look"},
		{ID: "e", Name: "fail", Arguments: "{}"},
		{ID: "f", Name: "look", Arguments: "null"},
	}}, llmtest.Reply{Text: "done"})
	r := &ran{}
	fail := Tool{Name: "fail", Run: func(context.Context, json.RawMessage) (string, error) { return "", errors.New("disk full") }}
	refused := map[string]bool{}
	res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent", Tools: []Tool{r.tool("look", "seen", false), fail},
		OnEvent: func(e Event) {
			if e.Kind == ToolFinished {
				refused[e.Call.ID] = e.Refused
			}
		}}, ask("go"))
	if err != nil {
		t.Fatal(err)
	}
	// What the loop settled without the tool is marked as such: an app audits
	// those, and only those, since a tool that ran records itself.
	for id, want := range map[string]bool{"a": true, "b": true, "c": true, "d": false, "e": false, "f": true} {
		if refused[id] != want {
			t.Errorf("call %s refused=%v", id, refused[id])
		}
	}
	want := map[string]string{"a": `no tool named "nope"`, "b": "not a JSON object", "c": "not a JSON object", "d": "seen", "e": "disk full", "f": "not a JSON object"}
	for _, m := range res.Messages {
		if m.Role != llm.ToolResult {
			continue
		}
		if !strings.Contains(m.Content, want[m.ToolCallID]) || m.Failed != (m.ToolCallID != "d") {
			t.Errorf("result for %s: %q failed=%v", m.ToolCallID, m.Content, m.Failed)
		}
		delete(want, m.ToolCallID)
	}
	if len(want) != 0 || strings.Join(r.args, "|") != "look {}" {
		t.Errorf("missing %v; ran %v: a call without arguments runs with an empty object", want, r.args)
	}
}

func TestADestructiveToolWaitsForTheUser(t *testing.T) {
	for name, c := range map[string]struct {
		confirm func(context.Context, llm.ToolCall) (bool, error)
		ran     bool
		says    string
	}{
		"no way to ask": {nil, false, "without asking"},
		"declined":      {func(context.Context, llm.ToolCall) (bool, error) { return false, nil }, false, "did not allow"},
		"allowed":       {func(context.Context, llm.ToolCall) (bool, error) { return true, nil }, true, "wiped"},
	} {
		p := model().Reply(calling("w", "wipe", `{"all":true}`), llmtest.Reply{Text: "ok"})
		r := &ran{}
		var asked []string
		confirm := c.confirm
		if confirm != nil {
			confirm = func(ctx context.Context, call llm.ToolCall) (bool, error) {
				asked = append(asked, call.Name+" "+call.Arguments)
				return c.confirm(ctx, call)
			}
		}
		res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent", Confirm: confirm, Tools: []Tool{r.tool("wipe", "wiped", true)}}, ask("wipe"))
		if err != nil {
			t.Fatal(err)
		}
		result := res.Messages[3]
		if (len(r.args) == 1) != c.ran || !strings.Contains(result.Content, c.says) || result.Failed == c.ran {
			t.Errorf("%s: ran %v, result %+v", name, r.args, result)
		}
		if c.confirm != nil && strings.Join(asked, "") != `wipe {"all":true}` {
			t.Errorf("%s: asked %v", name, asked)
		}
	}
	p := model().Reply(calling("w", "wipe", "{}"))
	broken := errors.New("no window to ask in")
	_, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent", Tools: []Tool{(&ran{}).tool("wipe", "", true)},
		Confirm: func(context.Context, llm.ToolCall) (bool, error) { return false, broken }}, ask("wipe"))
	if !errors.Is(err, broken) {
		t.Errorf("a question that could not be asked went on as a no: %v", err)
	}
}

// Measured on smollm2 through Ollama: refused once, it called the same tool
// again, and then another destructive one. Each retry would put another
// dialog in front of the user who just said no.
func TestADeclineHoldsForTheRestOfTheRun(t *testing.T) {
	p := model().Reply(calling("1", "wipe", `{"all":true}`), calling("2", "wipe", `{"all":false}`), calling("3", "trim", "{}"), llmtest.Reply{Text: "ok"})
	r := &ran{}
	var asked []string
	res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent",
		Tools: []Tool{r.tool("wipe", "wiped", true), r.tool("trim", "trimmed", true)},
		Confirm: func(_ context.Context, c llm.ToolCall) (bool, error) {
			asked = append(asked, c.Name)
			return c.Name == "trim", nil
		}}, ask("clean up"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(asked, ",") != "wipe,trim" || strings.Join(r.args, "|") != "trim {}" {
		t.Errorf("asked %v, ran %v", asked, r.args)
	}
	again := res.Messages[5]
	if !again.Failed || !strings.Contains(again.Content, "already") {
		t.Errorf("the second wipe got %+v", again)
	}
}

func TestTheRunStopsAtItsStepLimit(t *testing.T) {
	p := model().Reply(calling("1", "look", "{}"), calling("2", "look", "{}"), calling("3", "look", "{}"))
	res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent", MaxSteps: 2, Tools: []Tool{(&ran{}).tool("look", "x", false)}}, ask("loop"))
	if !errors.Is(err, ErrStepLimit) || res == nil || res.Steps != 2 || len(res.Messages) != 6 || res.Messages[5].Role != llm.ToolResult {
		t.Errorf("%v, %+v", err, res)
	}
	if n := len(p.Requests()); n != 2 {
		t.Errorf("%d requests", n)
	}
}

func TestTenStepsWhenNotSaid(t *testing.T) {
	p := model()
	for range 11 {
		p.Reply(calling("x", "look", "{}"))
	}
	res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent", Tools: []Tool{(&ran{}).tool("look", "x", false)}}, ask("loop"))
	if !errors.Is(err, ErrStepLimit) || res.Steps != 10 {
		t.Errorf("%v after %d steps", err, res.Steps)
	}
}

func TestAFailureKeepsWhatWasDone(t *testing.T) {
	down := errors.New("server gone")
	p := model().Reply(calling("1", "look", "{}"), llmtest.Reply{Err: down})
	res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent", Tools: []Tool{(&ran{}).tool("look", "x", false)}}, ask("go"))
	if !errors.Is(err, down) || res == nil || len(res.Messages) != 4 || res.Steps != 2 {
		t.Errorf("%v, %+v", err, res)
	}
}

// A run that stops mid-tool still has to hand back a conversation a provider
// will take: an assistant turn whose calls have no results is refused, and the
// app was told it can save what came back and go on from there.
func TestAStoppedRunLeavesNoCallUnanswered(t *testing.T) {
	p := model().Reply(llmtest.Reply{ToolCalls: []llm.ToolCall{
		{ID: "1", Name: "slow", Arguments: "{}"},
		{ID: "2", Name: "slow", Arguments: "{}"},
	}}, llmtest.Reply{Text: "never"})
	ctx, cancel := context.WithCancel(context.Background())
	slow := Tool{Name: "slow", Run: func(context.Context, json.RawMessage) (string, error) { cancel(); return "late", nil }}
	var events []string
	res, err := Run(ctx, Options{Registry: rig(t, p), Slot: "agent", Tools: []Tool{slow},
		OnEvent: func(e Event) { events = append(events, string(e.Kind)+" "+e.Call.ID) }}, ask("go"))
	if !errors.Is(err, context.Canceled) || len(p.Requests()) != 1 {
		t.Fatalf("%v after %d requests", err, len(p.Requests()))
	}
	if res == nil || len(res.Messages) != 5 {
		t.Fatalf("%+v", res)
	}
	// The first tool ran before the stop reached it, so its answer stands; the
	// second never ran and says so.
	if m := res.Messages[3]; m.Role != llm.ToolResult || m.ToolCallID != "1" || m.Failed || m.Content != "late" {
		t.Errorf("the finished tool: %+v", m)
	}
	if m := res.Messages[4]; m.Role != llm.ToolResult || m.ToolCallID != "2" || !m.Failed || !strings.Contains(m.Content, "stopped") {
		t.Errorf("the tool that never ran: %+v", m)
	}
	// A tool that started is reported finished, or a UI keeps a spinner.
	if strings.Join(events, "|") != "tool_started 1|tool_finished 1" {
		t.Errorf("events %q", events)
	}
}

// A tool that fails because the run was stopped is a stop, not one more
// failed call to hand back to the model and carry on from.
func TestAToolFailingOnTheWayOutStopsTheRun(t *testing.T) {
	p := model().Reply(calling("1", "slow", "{}"), llmtest.Reply{Text: "never"})
	ctx, cancel := context.WithCancel(context.Background())
	slow := Tool{Name: "slow", Run: func(context.Context, json.RawMessage) (string, error) {
		cancel()
		return "", errors.New("gave up")
	}}
	res, err := Run(ctx, Options{Registry: rig(t, p), Slot: "agent", Tools: []Tool{slow}}, ask("go"))
	if !errors.Is(err, context.Canceled) || len(p.Requests()) != 1 {
		t.Fatalf("%v after %d requests", err, len(p.Requests()))
	}
	if m := res.Messages[3]; m.Content != "gave up" || !m.Failed {
		t.Errorf("%+v: what the tool said is still what happened", m)
	}
}

// The contract says a Result always comes back, so an app can render it.
func TestARunThatCannotStartStillAnswersWithAResult(t *testing.T) {
	reg := rig(t, model())
	for name, opt := range map[string]Options{
		"unbound slot": {Registry: reg, Slot: "other"},
		"no registry":  {Slot: "agent"},
	} {
		res, err := Run(context.Background(), opt, ask("x"))
		if err == nil || res == nil || len(res.Messages) != 2 {
			t.Errorf("%s: %+v, %v", name, res, err)
		}
	}
}

func TestWhatCannotStartSaysWhy(t *testing.T) {
	p := model()
	reg := rig(t, p)
	look := (&ran{}).tool("look", "", false)
	for name, c := range map[string]struct {
		opt Options
		req llm.ChatRequest
	}{
		"tools in the request": {Options{Registry: reg, Slot: "agent"}, llm.ChatRequest{Tools: []llm.Tool{{Name: "x"}}}},
		"two tools, one name":  {Options{Registry: reg, Slot: "agent", Tools: []Tool{look, look}}, ask("x")},
		"no registry":          {Options{Slot: "agent"}, ask("x")},
		"unbound slot":         {Options{Registry: reg, Slot: "other"}, ask("x")},
	} {
		if _, err := Run(context.Background(), c.opt, c.req); err == nil {
			t.Errorf("%s: ran", name)
		}
	}
	if _, err := Run(context.Background(), Options{Registry: reg, Slot: "other"}, ask("x")); !errors.Is(err, llm.ErrNoBinding) {
		t.Errorf("unbound: %v", err)
	}
	if n := len(p.Requests()); n != 0 {
		t.Errorf("%d requests", n)
	}
}

// Says its window, as Anthropic and Ollama do.
type describing struct {
	*llmtest.Provider
	window int
	asked  int
	model  string
}

func (d *describing) Describe(_ context.Context, model string) (*llm.ModelDetail, error) {
	d.asked++
	d.model = model
	return &llm.ModelDetail{ContextLength: d.window}, nil
}

func long(role llm.MessageRole, tag string) llm.Message {
	return llm.Message{Role: role, Content: tag + strings.Repeat(" word", 60)}
}

// What is sent is cut to the window; what the run returns is not.
func TestTheWindowTrimsWhatIsSentNotWhatIsKept(t *testing.T) {
	d := &describing{Provider: model().Reply(calling("1", "look", "{}"), llmtest.Reply{Text: "ok"}), window: 300}
	history := []llm.Message{{Role: llm.System, Content: "Be brief."}, long(llm.User, "u1"), long(llm.Assistant, "a1"), long(llm.User, "u2"), long(llm.Assistant, "a2"), {Role: llm.User, Content: "now?"}}
	res, err := Run(context.Background(), Options{Registry: rig(t, d), Slot: "agent", Tools: []Tool{(&ran{}).tool("look", "x", false)}}, llm.ChatRequest{Messages: history})
	if err != nil {
		t.Fatal(err)
	}
	if d.asked != 1 || d.model != "m" || res.Dropped != 2 || len(res.Messages) != 9 || !strings.HasPrefix(res.Messages[1].Content, "u1") {
		t.Errorf("asked %d about %q, dropped %d, kept %d: the model is the slot's when the request names none", d.asked, d.model, res.Dropped, len(res.Messages))
	}
	for i, req := range d.Requests() {
		var sent []string
		for _, m := range req.Messages {
			sent = append(sent, strings.SplitN(m.Content, " ", 2)[0])
		}
		if got := strings.Join(sent, ","); got != []string{"Be,u2,a2,now?", "Be,u2,a2,now?,,x"}[i] {
			t.Errorf("request %d sent %s", i, got)
		}
	}
	// A window the app names wins over the provider's.
	d2 := &describing{Provider: model().Reply(llmtest.Reply{Text: "ok"}), window: 300}
	if res, err := Run(context.Background(), Options{Registry: rig(t, d2), Slot: "agent", Window: 100000}, llm.ChatRequest{Messages: history}); err != nil || res.Dropped != 0 || d2.asked != 0 {
		t.Errorf("%v, dropped %d, asked %d", err, res.Dropped, d2.asked)
	}
	// Room for the reply comes from MaxTokens, then from an eighth of the
	// window: ask("x") estimates 12 tokens, which fill a 12 token window.
	for name, c := range map[string]struct {
		window, maxTokens int
		req               llm.ChatRequest
	}{"max tokens": {300, 300, llm.ChatRequest{MaxTokens: 300, Messages: history}}, "an eighth": {12, 0, ask("x")}} {
		d3 := &describing{Provider: model().Reply(llmtest.Reply{Text: "ok"}), window: c.window}
		if _, err := Run(context.Background(), Options{Registry: rig(t, d3), Slot: "agent"}, c.req); !errors.Is(err, llm.ErrContextFull) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestTheCallersMessagesAreNotWrittenTo(t *testing.T) {
	p := model().Reply(llmtest.Reply{Text: "ok"})
	history := make([]llm.Message, 1, 8)
	history[0] = llm.Message{Role: llm.User, Content: "hi"}
	if _, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent"}, llm.ChatRequest{Messages: history}); err != nil {
		t.Fatal(err)
	}
	if spare := history[:2][1]; spare.Role != "" {
		t.Errorf("the run wrote into the caller's slice: %+v", spare)
	}
}

func TestAProviderThatCannotSayItsWindowIsSentEverything(t *testing.T) {
	p := model().Reply(llmtest.Reply{Text: "ok"})
	history := []llm.Message{long(llm.User, "u1"), long(llm.Assistant, "a1"), {Role: llm.User, Content: "now?"}}
	res, err := Run(context.Background(), Options{Registry: rig(t, p), Slot: "agent"}, llm.ChatRequest{Messages: history})
	if err != nil || res.Dropped != 0 || len(p.Requests()[0].Messages) != 3 {
		t.Errorf("%v, %+v", err, res)
	}
}
