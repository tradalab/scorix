package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestUsageAddsUpAcrossRequests(t *testing.T) {
	a := Usage{PromptTokens: 1, CacheReadTokens: 2, CacheWriteTokens: 3, CompletionTokens: 4, PromptDuration: 5, GenerateDuration: 6}
	if got := a.Add(a); got != (Usage{2, 4, 6, 8, 10, 12}) {
		t.Errorf("%+v", got)
	}
}

func TestCostBillsEachKindOfTokenAtItsRate(t *testing.T) {
	u := Usage{PromptTokens: 2572, CacheReadTokens: 2000, CacheWriteTokens: 100, CompletionTokens: 89}
	for name, c := range map[string]struct {
		p    Price
		want float64
	}{
		"split":                 {Price{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, (472*3 + 2000*0.3 + 100*3.75 + 89*15) / 1e6},
		"cache billed as input": {Price{Input: 3, Output: 15}, (2572*3 + 89*15) / 1e6},
	} {
		if got := u.Cost(c.p); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	odd := Usage{CacheReadTokens: 10}
	if got := odd.Cost(Price{Input: 1, CacheRead: 1}); math.Abs(got-10e-6) > 1e-12 {
		t.Errorf("cached tokens without a total: %v", got)
	}
}

type describer struct {
	bare
	window int
	err    error
}

func (d describer) Describe(context.Context, string) (*ModelDetail, error) {
	if d.err != nil {
		return nil, d.err
	}
	return &ModelDetail{ContextLength: d.window}, nil
}

type bare struct{}

func (bare) ID() string                 { return "f" }
func (bare) Capabilities() []Capability { return []Capability{Chat} }

// Reports what the model would: here, twice the estimate, as a tokenizer
// denser than the three characters a token the estimate assumes.
type counter struct {
	bare
	calls int
	fail  bool
}

func (c *counter) CountTokens(_ context.Context, req ChatRequest) (int, error) {
	c.calls++
	if c.fail {
		return 0, errors.New("count refused")
	}
	return 2 * EstimateTokens(req), nil
}

func TestContextWindowComesFromTheProvider(t *testing.T) {
	ctx := context.Background()
	if n, err := ContextWindow(ctx, describer{window: 200000}, "m"); n != 200000 || err != nil {
		t.Errorf("%d, %v", n, err)
	}
	if n, err := ContextWindow(ctx, bare{}, "m"); n != 0 || err != nil {
		t.Errorf("a provider that cannot say: %d, %v", n, err)
	}
	down := errors.New("down")
	if _, err := ContextWindow(ctx, describer{err: down}, "m"); !errors.Is(err, down) {
		t.Errorf("%v", err)
	}
}

// k tokens of text under the estimate, plus the per-message overhead of 4.
func msg(role MessageRole, k int) Message {
	return Message{Role: role, Content: strings.Repeat("a", 3*k-2)}
}

func call() Message {
	return Message{Role: Assistant, ToolCalls: []ToolCall{{ID: "c", Name: "t", Arguments: "{}"}}}
}

func TestEstimateCountsEverythingThatIsSent(t *testing.T) {
	base := ChatRequest{Messages: []Message{msg(User, 10)}}
	if n := EstimateTokens(base); n != 14 {
		t.Fatalf("%d", n)
	}
	for name, change := range map[string]func(*ChatRequest){
		"image":     func(r *ChatRequest) { r.Messages[0].Images = []Image{{MIME: "image/png"}} },
		"audio":     func(r *ChatRequest) { r.Messages[0].Audio = []AudioClip{{Data: make([]byte, 5000)}} },
		"tool call": func(r *ChatRequest) { r.Messages[0].ToolCalls = []ToolCall{{Name: "look", Arguments: `{"q":"x"}`}} },
		"replay":    func(r *ChatRequest) { r.Messages[0].Replay = &Replay{Data: json.RawMessage(strings.Repeat("x", 400))} },
		"tool": func(r *ChatRequest) {
			r.Tools = []Tool{{Name: "look", Description: "Looks", Parameters: json.RawMessage(`{"type":"object"}`)}}
		},
		"format": func(r *ChatRequest) { r.Format = &ResponseFormat{Schema: json.RawMessage(`{"type":"object"}`)} },
	} {
		r := ChatRequest{Messages: []Message{msg(User, 10)}}
		change(&r)
		if EstimateTokens(r) <= 14 {
			t.Errorf("%s is not counted", name)
		}
	}
	if n := EstimateTokens(ChatRequest{Messages: []Message{{Role: User, Images: []Image{{}, {}}}}}); n != 4+2*perImage {
		t.Errorf("two images: %d", n)
	}
	// Characters, not bytes: Vietnamese takes two or three bytes a letter.
	if vi, en := estimateText("ắằẳẵặ"), estimateText("aaaaa"); vi != en {
		t.Errorf("%d tokens for five Vietnamese letters, %d for five English ones", vi, en)
	}
}

func TestAConversationThatFitsIsLeftAlone(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 10), msg(Assistant, 10), msg(User, 10)}}
	f, err := Fit(context.Background(), bare{}, req, 1000, 100)
	if err != nil || f.Dropped != 0 || len(f.Request.Messages) != 4 || f.Tokens != 56 || f.Counted {
		t.Errorf("%+v, %v", f, err)
	}
}

// 394 estimated tokens; 285 fit. Dropping the first user turn leaves 290, so
// the tool call goes next, and its two results with it: the call alone would
// have been enough, and would have left two results answering nothing.
func TestTheOldestTurnsGoFirstAndAToolCallTakesItsResults(t *testing.T) {
	req := ChatRequest{Messages: []Message{
		msg(System, 10), msg(User, 100), call(), msg(ToolResult, 100), msg(ToolResult, 100),
		msg(User, 10), msg(Assistant, 10), msg(User, 10), call(), msg(ToolResult, 10),
	}}
	if n := EstimateTokens(req); n != 394 {
		t.Fatalf("fixture estimates %d", n)
	}
	f, err := Fit(context.Background(), bare{}, req, 300, 15)
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{req.Messages[0], req.Messages[5], req.Messages[6], req.Messages[7], req.Messages[8], req.Messages[9]}
	if f.Dropped != 4 || f.Tokens != 76 || !sameMessages(f.Request.Messages, want) {
		t.Errorf("dropped %d, %d tokens, kept %s", f.Dropped, f.Tokens, roles(f.Request.Messages))
	}
	if len(req.Messages) != 10 {
		t.Error("the caller's request was changed")
	}
}

// An agent run adds turn after turn without a new question, so protecting
// everything after the last user message would leave nothing to drop and kill
// the run on the step that overflowed. The turn in progress stays whole; the
// tool exchanges before it go, oldest first, in whole pairs.
func TestALongToolRunDropsItsOldestExchanges(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 100)}}
	for range 4 {
		// The assistant says something as well as calling: a turn small enough
		// to drop on its own would never show a result left without its call.
		said := msg(Assistant, 100)
		said.ToolCalls = call().ToolCalls
		req.Messages = append(req.Messages, said, msg(ToolResult, 100))
	}
	if n := EstimateTokens(req); n != 958 {
		t.Fatalf("fixture estimates %d", n)
	}
	for name, c := range map[string]struct {
		limit, dropped, tokens int
		roles                  string
	}{
		"room for the turn in progress": {350, 6, 328, "system user assistant tool"},
		"room for one exchange more":    {560, 4, 538, "system user assistant tool assistant tool"},
		"budget met mid-exchange":       {642, 4, 538, "system user assistant tool assistant tool"},
	} {
		f, err := Fit(context.Background(), bare{}, req, c.limit, 0)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if f.Dropped != c.dropped || f.Tokens != c.tokens || roles(f.Request.Messages) != c.roles {
			t.Errorf("%s: dropped %d, %d tokens, kept %s", name, f.Dropped, f.Tokens, roles(f.Request.Messages))
		}
	}
}

func TestAfterTheCutTheConversationOpensWithTheUser(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 100), msg(Assistant, 10), msg(User, 10), msg(Assistant, 10), msg(User, 10)}}
	f, err := Fit(context.Background(), bare{}, req, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if f.Dropped != 2 || roles(f.Request.Messages) != "system user assistant user" {
		t.Errorf("dropped %d, kept %s", f.Dropped, roles(f.Request.Messages))
	}
}

// The counter says 680 where the estimate says 340: going by the estimate,
// nothing would be dropped; scaling it drops exactly two turns, where the
// estimate alone, once over, would take three.
func TestACounterCorrectsTheEstimate(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 100), msg(User, 100), msg(User, 100), msg(User, 10)}}
	ctx := context.Background()
	c := &counter{}
	f, err := Fit(ctx, c, req, 400, 0)
	if err != nil {
		t.Fatal(err)
	}
	// One count settled it: what is left is far enough under the limit that
	// the free estimate answers, so Tokens is that estimate.
	if f.Dropped != 2 || f.Tokens != 132 || c.calls != 1 || f.Counted {
		t.Errorf("counted %v, dropped %d, %d tokens, %d counts", f.Counted, f.Dropped, f.Tokens, c.calls)
	}
	if plain, _ := Fit(ctx, bare{}, req, 400, 0); plain.Dropped != 0 {
		t.Errorf("without a counter, dropped %d", plain.Dropped)
	}
	broken := &counter{fail: true}
	if f, err := Fit(ctx, broken, req, 400, 0); err != nil || f.Counted || f.Dropped != 0 {
		t.Errorf("a counter that fails is an estimate: %+v, %v", f, err)
	}
}

// Answers from a list, as a tokenizer whose ratio to the estimate shifts with
// what is dropped: the first cut is not enough, and a second round follows.
type script struct {
	bare
	counts []int
	calls  int
}

func (s *script) CountTokens(context.Context, ChatRequest) (int, error) {
	n := s.counts[min(s.calls, len(s.counts)-1)]
	s.calls++
	return n, nil
}

func TestASecondCountCanCallForASecondCut(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10)}}
	for range 6 {
		req.Messages = append(req.Messages, msg(User, 100))
	}
	s := &script{counts: []int{1000, 500, 300}}
	f, err := Fit(context.Background(), s, req, 400, 0)
	if err != nil || f.Dropped != 5 || f.Tokens != 118 || s.calls != 2 {
		t.Errorf("%+v, %v after %d counts", f, err, s.calls)
	}
}

// Counting costs a request that uploads the whole conversation, images and
// all. A conversation the free estimate already proves has room does not pay
// for one; the estimate errs high, and the margin covers a tokenizer denser
// than it assumes.
func TestNothingIsCountedWhenTheEstimateAlreadyProvesRoom(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 10), msg(Assistant, 10), msg(User, 10)}}
	c := &counter{}
	f, err := Fit(context.Background(), c, req, 1000, 100)
	if err != nil || c.calls != 0 || f.Counted || f.Tokens != 56 || f.Dropped != 0 {
		t.Errorf("%+v, %v after %d counts", f, err, c.calls)
	}
	// Close to the limit it counts, because the estimate is the thing in doubt.
	near := &counter{}
	if _, err := Fit(context.Background(), near, req, 60, 0); err != nil || near.calls == 0 {
		t.Errorf("%v after %d counts", err, near.calls)
	}
}

// The counter's ratio to the estimate moves as the conversation shrinks, so
// each round is measured again rather than scaled by what the first one said.
// Running out of rounds with turns still droppable used to end the run.
func TestARunOfRoundsEndsByDroppingWhatIsLeft(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10)}}
	for range 20 {
		req.Messages = append(req.Messages, msg(User, 100))
	}
	s := &script{counts: []int{2000, 1800, 1600, 1400, 700}}
	f, err := Fit(context.Background(), s, req, 1000, 0)
	if err != nil {
		t.Fatalf("%v: turns were left to drop", err)
	}
	if f.Tokens > 1000 || f.Dropped == 0 || s.calls > 5 {
		t.Errorf("%d tokens, dropped %d, %d counts", f.Tokens, f.Dropped, s.calls)
	}
}

// Nothing to drop is not a reason to pay for a second count.
func TestNothingToDropIsCountedOnce(t *testing.T) {
	huge := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 1000)}}
	c := &counter{}
	if _, err := Fit(context.Background(), c, huge, 500, 0); !errors.Is(err, ErrContextFull) || c.calls != 1 {
		t.Errorf("%v after %d counts", err, c.calls)
	}
}

// A conversation that arrives without a user turn at all: the promise is that
// what is sent opens with one, so what cannot is dropped rather than refused
// by the API with a raw 400.
func TestAConversationWithNoQuestionKeepsOnlyWhatMustStay(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(System, 10), call(), msg(ToolResult, 100), msg(Assistant, 100)}}
	f, err := Fit(context.Background(), bare{}, req, 150, 0)
	if err != nil {
		t.Fatal(err)
	}
	if roles(f.Request.Messages) != "system assistant" || f.Dropped != 2 {
		t.Errorf("dropped %d, kept %s", f.Dropped, roles(f.Request.Messages))
	}
}

func TestACountOfZeroIsNoCount(t *testing.T) {
	req := ChatRequest{Messages: []Message{msg(User, 10)}}
	// Close enough to the limit that it does count, and the count says zero.
	s := &script{counts: []int{0}}
	f, err := Fit(context.Background(), s, req, 20, 0)
	if err != nil || f.Counted || f.Tokens != 14 || s.calls != 1 {
		t.Errorf("%+v, %v after %d counts", f, err, s.calls)
	}
}

func TestWhatCannotFitSaysSo(t *testing.T) {
	ctx := context.Background()
	huge := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 1000)}}
	if _, err := Fit(ctx, bare{}, huge, 500, 0); !errors.Is(err, ErrContextFull) {
		t.Errorf("a last turn larger than the window: %v", err)
	}
	// Dropping the old question would fit only by taking the new one, and the
	// tool call answering it, too.
	last := ChatRequest{Messages: []Message{msg(System, 10), msg(User, 100), msg(User, 10), call(), msg(ToolResult, 10)}}
	if f, err := Fit(ctx, bare{}, last, 40, 0); !errors.Is(err, ErrContextFull) {
		kept := ""
		if f != nil {
			kept = roles(f.Request.Messages)
		}
		t.Errorf("the last user turn was cut: %s, %v", kept, err)
	}
	small := ChatRequest{Messages: []Message{msg(User, 1)}}
	if _, err := Fit(ctx, bare{}, small, 100, 100); !errors.Is(err, ErrContextFull) || !strings.Contains(err.Error(), "reserved") {
		t.Errorf("a reserve that takes the whole window: %v", err)
	}
	if _, err := Fit(ctx, bare{}, small, 0, 0); err == nil || errors.Is(err, ErrContextFull) {
		t.Errorf("an unknown window is not a full one: %v", err)
	}
}

func sameMessages(a, b []Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content || len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
	}
	return true
}

func roles(ms []Message) string {
	var out []string
	for _, m := range ms {
		out = append(out, string(m.Role))
	}
	return strings.Join(out, " ")
}
