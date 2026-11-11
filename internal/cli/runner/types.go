package runner

import (
	"io"

	"github.com/tradalab/scorix/idl"
)

// DriftItem is one generated file that --check found out of sync.
type DriftItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"` // missing | out of date | model markers out of date
}

// GenerateResult is what `generate proto|model` reports. Check mode fills Drift
// and leaves the counters at zero; a real run is the other way round.
type GenerateResult struct {
	Check   bool `json:"check"`
	Files   int  `json:"files"`
	Created int  `json:"created"`
	Updated int  `json:"updated"`
	Skipped int  `json:"skipped"`
	// Rendered and identical to what is on disk, so nothing was written.
	Unchanged int         `json:"unchanged"`
	Drift     []DriftItem `json:"drift,omitempty"`
	Regen     string      `json:"regen,omitempty"` // the command that fixes the drift
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
	Command        string   `json:"command"`
	Request        string   `json:"request"`
	Reply          string   `json:"reply"`
	Arity          string   `json:"arity"` // unary | server-stream
	Middleware     []string `json:"middleware,omitempty"`
	MCP            bool     `json:"mcp,omitempty"` // the @mcp opt-in
	MCPDestructive bool     `json:"mcp_destructive,omitempty"`
	CLI            bool     `json:"cli,omitempty"` // the @cli opt-in
	CLIDestructive bool     `json:"cli_destructive,omitempty"`
	CLIHeadless    bool     `json:"cli_headless,omitempty"`
	CLIExec        bool     `json:"cli_exec,omitempty"`
	CLIArg         string   `json:"cli_arg,omitempty"`
	CLIRest        string   `json:"cli_rest,omitempty"`
	CLISecret      string   `json:"cli_secret,omitempty"`
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

type (
	protoFile    = idl.File
	protoMessage = idl.Message
	protoField   = idl.Field
	protoService = idl.Service
	protoRPC     = idl.RPC
)

type protoTemplateData struct {
	Module    string
	Proto     protoFile
	Services  []protoService
	OutEvents []protoRPC
	InEvents  []protoRPC
	HasEvents bool
	HasMCP    bool // gates scorixMCP, so an app without @mcp regenerates unchanged
	HasCLI    bool // gates the CLI column, for the same reason
	Shell     ShellKind

	Natives        []protoService
	HasNativeRoute bool // an `@native expose` service, so handler.go imports internal/host
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
