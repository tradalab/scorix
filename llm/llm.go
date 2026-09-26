// Package llm is the capability layer an app asks for chat and embeddings
// through. The app names its providers and its slots; nothing here assumes a
// particular engine or a fixed set of roles.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type Capability string

const (
	Chat   Capability = "chat"
	Embed  Capability = "embed"
	Vision Capability = "vision"
	Audio  Capability = "audio"
	Tools  Capability = "tools"
	// Speech to text, a model of its own rather than chat that hears.
	Transcribe Capability = "transcribe"
)

type MessageRole string

const (
	System     MessageRole = "system"
	User       MessageRole = "user"
	Assistant  MessageRole = "assistant"
	ToolResult MessageRole = "tool"
)

type Message struct {
	Role    MessageRole
	Content string
	Images  []Image
	Audio   []AudioClip
	// On an assistant message: the calls it made, sent back so the model sees
	// its own turn when the conversation continues.
	ToolCalls []ToolCall
	// On a ToolResult message: which call this answers.
	ToolCallID string
	// On a ToolResult message: the call failed and Content says why, so the
	// model can correct itself rather than read the error as data.
	Failed bool
	// On an assistant message: what the provider that wrote it needs back
	// unchanged when the conversation continues.
	Replay *Replay
}

// Opaque to everyone but the driver named here; other drivers ignore it, so a
// conversation can move to another provider and simply lose it.
type Replay struct {
	Driver string
	Data   json.RawMessage
}

type Image struct {
	MIME string
	Data []byte
}

type AudioClip struct {
	MIME string
	Data []byte
}

type Tool struct {
	Name        string
	Description string
	// JSON Schema of the arguments object. SchemaFor builds one from a Go type.
	Parameters json.RawMessage
}

type ToolCall struct {
	ID   string
	Name string
	// As the model wrote it. Models do write invalid JSON, so decoding is the
	// app's call, with Decode or otherwise.
	Arguments string
}

func (c ToolCall) Decode(v any) error {
	if err := json.Unmarshal([]byte(c.Arguments), v); err != nil {
		return fmt.Errorf("tool %s: arguments: %w", c.Name, err)
	}
	return nil
}

// Empty lets the model decide. Any value other than the constants names the one
// tool the model must call.
type ToolChoice string

const (
	ToolAuto     ToolChoice = ""
	ToolNone     ToolChoice = "none"
	ToolRequired ToolChoice = "required"
)

type ResponseFormat struct {
	Name string
	// JSON Schema the reply must satisfy. Whether the server enforces it is up
	// to the server.
	Schema json.RawMessage
}

type ChatRequest struct {
	// Empty takes the model bound to the slot.
	Model       string
	Messages    []Message
	MaxTokens   int
	Temperature *float64
	Stop        []string
	Tools       []Tool
	ToolChoice  ToolChoice
	// Nil for free text.
	Format *ResponseFormat
	// Asks the provider to charge the next request for reading the conversation
	// back rather than reading it again; Usage.CacheReadTokens says how much
	// came from it. Worth most in an agent loop, which resends everything every
	// step. Anthropic needs the request to mark where the reusable part ends,
	// so the driver marks it; the others cache by themselves and ignore this.
	Cache bool
	// Provider-specific parameters, sent as they are; a typed field above wins
	// over the same key here.
	Extra map[string]any
}

// Reasoning and tool calls arrive on their own channels, so an app can fold
// the scratch work in or drop it.
type ChunkKind string

const (
	ChunkText      ChunkKind = "text"
	ChunkReasoning ChunkKind = "reasoning"
	// Sent once per call, complete: a half-streamed argument string is of no use
	// to anyone.
	ChunkToolCall ChunkKind = "tool_call"
)

type Chunk struct {
	Kind ChunkKind
	Text string
	// Set when Kind is ChunkToolCall.
	Call *ToolCall
}

type ChatResult struct {
	Model        string
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        Usage
	Replay       *Replay
}

// The assistant turn to append before the tool results; building it by hand
// is how Replay gets dropped.
func (r *ChatResult) Message() Message {
	return Message{Role: Assistant, Content: r.Content, ToolCalls: r.ToolCalls, Replay: r.Replay}
}

type Usage struct {
	// Input tokens, cached ones included where the provider reports caching
	// apart (Anthropic, OpenAI), so it reads as how full the window was.
	PromptTokens int
	// The part of PromptTokens served from, or written to, the provider's
	// prompt cache; each is billed at its own rate.
	CacheReadTokens  int
	CacheWriteTokens int
	CompletionTokens int
	// Measured by the server, so they exclude network and queueing.
	PromptDuration   time.Duration
	GenerateDuration time.Duration
}

type EmbedRequest struct {
	Model string
	Input []string
}

type EmbedResult struct {
	Model   string
	Vectors [][]float32
	Usage   Usage
}

// Beyond ID, each field is empty when the server does not say; the OpenAI API
// says only who owns a model.
type ModelInfo struct {
	ID            string
	OwnedBy       string
	Size          int64
	Modified      time.Time
	Digest        string
	Family        string
	ParameterSize string
	Quantization  string
}

type Provider interface {
	ID() string
	Capabilities() []Capability
}

// Optional. A provider that does not implement it counts as remote, so a slot
// marked LocalOnly fails closed on anything that has not said where it runs.
type Localer interface {
	Local() bool
}

func IsLocal(p Provider) bool {
	l, ok := p.(Localer)
	return ok && l.Local()
}

// An error from onChunk stops the generation: an app that streams to its own
// proto rpc has to stop paying for tokens once the client is gone.
type Chatter interface {
	Chat(ctx context.Context, req ChatRequest, onChunk func(Chunk) error) (*ChatResult, error)
}

type Embedder interface {
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResult, error)
}

type ModelLister interface {
	Models(ctx context.Context) ([]ModelInfo, error)
}

type TranscribeRequest struct {
	// Empty takes the model bound to the slot.
	Model string
	Audio AudioClip
	// Empty lets the model detect it.
	Language string
}

type TranscribeResult struct {
	Text     string
	Language string
}

type Transcriber interface {
	Transcribe(ctx context.Context, req TranscribeRequest) (*TranscribeResult, error)
}

type PullProgress struct {
	Status string
	// The layer this line is about, for a runtime that pulls several.
	Digest      string
	Done, Total int64
}

type ModelDetail struct {
	ID string
	// Input tokens the model can read; zero when the provider does not say.
	ContextLength int
	// The largest reply it can write; zero when the provider does not say.
	MaxOutputTokens int
	// What the runtime says the model can do. Never guessed from the name: a
	// guess is how a text model ends up sent a picture.
	Capabilities  []Capability
	Family        string
	ParameterSize string
	Quantization  string
}

type Describer interface {
	Describe(ctx context.Context, model string) (*ModelDetail, error)
}

// What a runtime that keeps models on this machine offers; a hosted API has
// none of it.
type ModelManager interface {
	Pull(ctx context.Context, model string, progress func(PullProgress)) error
	Describer
	Delete(ctx context.Context, model string) error
}

// Optional: a provider whose API counts a request's input tokens. Without it
// a budget can only estimate, and the estimate is off by the tokenizer.
type TokenCounter interface {
	CountTokens(ctx context.Context, req ChatRequest) (int, error)
}

func Has(p Provider, c Capability) bool {
	return slices.Contains(p.Capabilities(), c)
}
