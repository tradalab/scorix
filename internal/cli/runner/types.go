package runner

import "io"

// DriftItem is one generated file that --check found out of sync.
type DriftItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // missing | out of date | model markers out of date
}

// GenerateResult is what `generate proto|model` reports. Check mode fills Drift
// and leaves the counters at zero; a real run is the other way round.
type GenerateResult struct {
	Check   bool        `json:"check"`
	Files   int         `json:"files"`
	Created int         `json:"created"`
	Updated int         `json:"updated"`
	Skipped int         `json:"skipped"`
	Drift   []DriftItem `json:"drift,omitempty"`
	Regen   string      `json:"regen,omitempty"` // the command that fixes the drift
	// Nil from `generate model`: it has no IPC to describe.
	Surface *IPCSurface `json:"surface,omitempty"`
}

type IPCSurface struct {
	Package  string       `json:"package"`
	Services []IPCService `json:"services"`
}

type IPCService struct {
	Name string `json:"name"`
	// lowerCamel: service ConnGroup answers on connGroup:<method>.
	Wire     string       `json:"wire"`
	Commands []IPCCommand `json:"commands,omitempty"`
	Events   []IPCEvent   `json:"events,omitempty"`
}

type IPCCommand struct {
	Command    string   `json:"command"`
	Request    string   `json:"request"`
	Reply      string   `json:"reply"`
	Arity      string   `json:"arity"` // unary | server-stream
	Middleware []string `json:"middleware,omitempty"`
	MCP        bool     `json:"mcp,omitempty"` // the @mcp opt-in
}

type IPCEvent struct {
	Topic     string `json:"topic"`
	Direction string `json:"direction"` // out = Go pushes to JS, in = JS pushes to Go
	Payload   string `json:"payload"`
	Go        string `json:"go"`
	Frontend  string `json:"frontend"`
}

type GenerateProtoOptions struct {
	Proto string
	Dir   string
	Force bool
	// Check renders in memory and diffs against disk instead of writing, erroring
	// on drift - CI guard against editing proto (or generated files) without regen.
	Check   bool
	JSONOut io.Writer // non-nil switches the result to one JSON document on this writer
}

type protoFile struct {
	Package       string
	HasEmpty      bool
	HasMiddleware bool
	Middlewares   []string
	Messages      []protoMessage
	Services      []protoService
}

type protoMessage struct {
	Name   string
	GoName string
	Fields []protoField
}

type protoField struct {
	Name     string
	JSONName string
	GoName   string
	Type     string
	GoType   string
	TSType   string
	Repeated bool
}

type protoService struct {
	Name        string
	Package     string
	Middlewares []string
	RPCs        []protoRPC // request/reply commands only (events are split out)
	Events      []protoRPC // rpcs annotated @event / @event in
}

type protoRPC struct {
	Name          string
	LogicName     string
	MethodName    string
	FileName      string
	CommandName   string
	Middlewares   []string
	RequestType   string
	ResponseType  string
	RequestGoType string
	ResultGoType  string
	RequestTSType string
	ResultTSType  string

	// Arity is derived from the proto `stream` keyword on the request/response:
	// "unary" (1->1, default) or "server-stream" (1->N, response streamed). The
	// client-stream/bidi arities parse but are rejected by the generator and
	// reserved for hand-wired app.RegisterDuplex.
	Arity          string
	IsServerStream bool

	// Event fields - set when the rpc carries an @event / @broadcast annotation.
	// The request message is the event payload; the response type is ignored.
	IsEvent     bool
	EventDir    string // "out" (Go -> JS push, default) | "in" (JS -> Go one-way)
	EventName   string // wire topic, e.g. "monitor:message"
	EventGoName string // service-prefixed identifier, e.g. "MonitorMessage"

	// MCP opts this rpc in as a domain tool for an MCP client. Declared here and
	// carried in the surface; nothing serves it yet, and enrichProto refuses the
	// shapes a tool call cannot have (an event, or a stream).
	MCP bool
}

type protoTemplateData struct {
	Module    string
	Proto     protoFile
	Services  []protoService
	OutEvents []protoRPC
	InEvents  []protoRPC
	HasEvents bool
	Shell     ShellKind
}

type logicTemplateData struct {
	Module  string
	Service protoService
	RPC     protoRPC
}

type middlewareTemplateData struct {
	Module       string
	Name         string
	ExportedName string
}

type generatedFile struct {
	Path     string
	Template string
	Data     any
	Go       bool
	Force    bool
}
