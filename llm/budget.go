package llm

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// Input tokens the model reads, from the provider when it says; zero when it
// does not, which leaves the window to the app.
func ContextWindow(ctx context.Context, p Provider, model string) (int, error) {
	d, ok := p.(Describer)
	if !ok {
		return 0, nil
	}
	detail, err := d.Describe(ctx, model)
	if err != nil {
		return 0, err
	}
	return detail.ContextLength, nil
}

// Rough on purpose, and high rather than low: three characters a token where
// English runs near four, so an estimate errs toward dropping a turn too many
// rather than sending a request the server refuses.
func estimateText(s string) int { return (utf8.RuneCountInString(s) + 2) / 3 }

const (
	// How far under the limit an estimate has to be to skip a count.
	countMargin = 2

	perMessage = 4
	// Claude's price for an image near its largest size; smaller ones cost less.
	perImage = 1600
	// Gemini's rate for audio is 32 tokens a second, about one token a
	// kilobyte of 16 kHz 16-bit wav.
	audioBytesPerToken = 1000
)

func estimateMessage(m Message) int {
	n := perMessage + estimateText(m.Content) + perImage*len(m.Images)
	for _, a := range m.Audio {
		n += len(a.Data)/audioBytesPerToken + 1
	}
	for _, c := range m.ToolCalls {
		n += estimateText(c.Name) + estimateText(c.Arguments)
	}
	if m.Replay != nil {
		n += len(m.Replay.Data) / 4
	}
	return n
}

// What a request costs before its messages: tool definitions ride along with
// every request.
func estimateFixed(req ChatRequest) int {
	n := 0
	for _, t := range req.Tools {
		n += perMessage + estimateText(t.Name) + estimateText(t.Description) + len(t.Parameters)/3
	}
	if req.Format != nil {
		n += len(req.Format.Schema) / 3
	}
	return n
}

func EstimateTokens(req ChatRequest) int {
	n := estimateFixed(req)
	for _, m := range req.Messages {
		n += estimateMessage(m)
	}
	return n
}

type Fitted struct {
	Request ChatRequest
	// Input tokens of Request: counted by the provider when Counted, estimated
	// otherwise.
	Tokens  int
	Counted bool
	// Messages left out, oldest first.
	Dropped int
}

// Keeps the system messages, the last question and the turn in progress, and
// drops the oldest of the rest until the input leaves room for the reply. A
// tool call and the results answering it go together, and what is left before
// the question opens with a user turn: an API refuses either shape, and Claude
// refuses a latest turn whose signed thinking did not come back whole. A
// conversation that holds no user turn at all cannot be given one, and comes
// back as it was, minus what had to go.
// ErrContextFull when even the kept part does not fit.
func Fit(ctx context.Context, p Provider, req ChatRequest, window, reserve int) (*Fitted, error) {
	if window <= 0 {
		return nil, fmt.Errorf("llm: a context window of %d tokens", window)
	}
	limit := window - reserve
	if limit <= 0 {
		return nil, fmt.Errorf("llm: %d tokens reserved for the reply leave none of a %d token window: %w", reserve, window, ErrContextFull)
	}
	msgs := req.Messages
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == User {
			lastUser = i
			break
		}
	}
	// An agent run adds turn after turn without a new question, so keeping
	// everything after the last one would leave nothing to drop.
	final := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == ToolResult {
			continue
		}
		if msgs[i].Role == Assistant {
			final = i
		}
		break
	}
	groups := turns(msgs, final, lastUser)
	keep := make([]bool, len(msgs))
	est := make([]int, len(msgs))
	fixed := estimateFixed(req)
	for i, m := range msgs {
		keep[i] = true
		est[i] = estimateMessage(m)
	}
	estimate := func() int {
		n := fixed
		for i := range msgs {
			if keep[i] {
				n += est[i]
			}
		}
		return n
	}

	counter, canCount := p.(TokenCounter)
	fitted := &Fitted{}
	measure := func() int {
		guess := estimate()
		// Counting costs a request that uploads the conversation, images and
		// all. One the estimate already proves has room does not need it, and
		// the estimate errs high; the margin covers a tokenizer up to twice as
		// dense as the three characters a token it assumes. Past that the
		// provider's own refusal is the backstop.
		if canCount && guess*countMargin > limit {
			r := req
			r.Messages = kept(msgs, keep)
			// Zero is no count: trusted, it would send anything.
			if n, err := counter.CountTokens(ctx, r); err == nil && n > 0 {
				fitted.Counted = true
				return n
			}
		}
		fitted.Counted = false
		return guess
	}
	tokens := measure()
	next := 0
	for tokens > limit && next < len(groups) {
		// The counter is exact but costs a request; the estimate is free but
		// off by the tokenizer, and off by a different amount as the
		// conversation changes, so it is rescaled against each new count.
		scale := float64(tokens) / float64(max(estimate(), 1))
		// Entered only over the limit, so this drops at least one turn and the
		// rounds cannot stall.
		guess := float64(tokens)
		for guess > float64(limit) && next < len(groups) {
			for _, i := range groups[next] {
				keep[i] = false
				fitted.Dropped++
				guess -= float64(est[i]) * scale
			}
			next++
		}
		// Only before the question: after it the conversation already opens
		// with one, and what follows are whole exchanges.
		for next < len(groups) && groups[next][0] < lastUser && msgs[groups[next][0]].Role != User {
			for _, i := range groups[next] {
				keep[i] = false
				fitted.Dropped++
			}
			next++
		}
		tokens = measure()
	}
	if tokens > limit {
		return nil, fmt.Errorf("llm: %d input tokens with %d dropped messages, over the %d left of the window: %w", tokens, fitted.Dropped, limit, ErrContextFull)
	}
	fitted.Request = req
	fitted.Request.Messages = kept(msgs, keep)
	fitted.Tokens = tokens
	return fitted, nil
}

// What may be dropped, oldest first: everything but the system messages, the
// last question and the turn in progress. An assistant turn carries the tool
// results answering it, since neither is valid without the other.
func turns(msgs []Message, final, lastUser int) [][]int {
	var out [][]int
	for i := 0; i < final; i++ {
		if msgs[i].Role == System || i == lastUser {
			continue
		}
		g := []int{i}
		if msgs[i].Role == Assistant && len(msgs[i].ToolCalls) > 0 {
			for i+1 < final && msgs[i+1].Role == ToolResult {
				i++
				g = append(g, i)
			}
		}
		out = append(out, g)
	}
	return out
}

func kept(msgs []Message, keep []bool) []Message {
	out := make([]Message, 0, len(msgs))
	for i, m := range msgs {
		if keep[i] {
			out = append(out, m)
		}
	}
	return out
}
