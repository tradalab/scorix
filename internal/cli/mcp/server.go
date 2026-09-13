// Package mcp is the CLI's MCP tool table. The protocol lives in mcpcore, which
// the in-app dev server shares: one hand-rolled JSON-RPC loop, two tool tables.
package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/tradalab/scorix/internal/cli/runner"
	"github.com/tradalab/scorix/internal/mcpcore"
)

type response = mcpcore.Response

var protocolVersions = mcpcore.ProtocolVersions

const codeMethodNotFound = mcpcore.CodeMethodNotFound

// args is a defined type, not an alias, so the lowercase accessors this package
// already calls in ~40 places keep working. core() bridges the two.
type args mcpcore.Args

func (a args) str(key, def string) string       { return mcpcore.Args(a).Str(key, def) }
func (a args) truthy(key string) bool           { return mcpcore.Args(a).Truthy(key) }
func (a args) truthyOr(key string, d bool) bool { return mcpcore.Args(a).TruthyOr(key, d) }
func (a args) num(key string, def int) int      { return mcpcore.Args(a).Num(key, def) }
func (a args) strs(key string) []string         { return mcpcore.Args(a).Strs(key) }

// tool keeps unexported fields on purpose: this table is the CLI's own, and the
// literals here and in the tests stay put while the core takes exported ones.
type tool struct {
	name        string
	description string
	schema      map[string]any
	run         func(ctx context.Context, a args, out io.Writer) error
	timeout     time.Duration
	direct      bool
}

func (t tool) core() mcpcore.Tool {
	return mcpcore.Tool{
		Name:        t.name,
		Description: t.description,
		Schema:      t.schema,
		Run: func(ctx context.Context, a mcpcore.Args, out io.Writer) error {
			return t.run(ctx, args(a), out)
		},
		Timeout: t.timeout,
		Direct:  t.direct,
	}
}

type Server struct {
	version string
	tools   []tool
	dev     devJob

	// Assigned by Serve. Until then a status call has nothing to report, which is
	// the truth: nothing can be running before the loop starts.
	core *mcpcore.Server

	watchIn  io.Reader
	watchTee io.Writer
}

func NewServer(version string) *Server {
	s := &Server{version: version}
	s.tools = builtinTools(s)
	return s
}

// WatchOutput forwards what the runner prints as progress notifications.
func (s *Server) WatchOutput(r io.Reader, tee io.Writer) {
	s.watchIn, s.watchTee = r, tee
}

// Built here rather than in NewServer because a test appends to s.tools first.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	tools := make([]mcpcore.Tool, 0, len(s.tools))
	for _, t := range s.tools {
		tools = append(tools, t.core())
	}
	s.core = mcpcore.New(mcpcore.Options{
		Version:      s.version,
		Tools:        tools,
		Fallback:     errorEnvelope,
		OutputSchema: envelopeSchema,
	})
	if s.watchIn != nil {
		s.core.WatchOutput(s.watchIn, s.watchTee)
	}
	return s.core.Serve(ctx, in, out)
}

func (s *Server) running() []map[string]any {
	if s.core == nil {
		return []map[string]any{}
	}
	return s.core.Running()
}

// Reuses the runner's writer so the fallback cannot drift into a second envelope
// shape; a tool only lands here when it panicked before writing its own.
func errorEnvelope(command string, err error) string {
	if err == nil {
		err = errors.New("produced no result")
	}
	var b bytes.Buffer
	_ = runner.EmitJSON(&b, command, nil, err)
	return b.String()
}

func obj(props map[string]any) map[string]any { return mcpcore.Obj(props) }
func strProp(desc string) map[string]any      { return mcpcore.StrProp(desc) }
func boolProp(desc string) map[string]any     { return mcpcore.BoolProp(desc) }
func intProp(desc string) map[string]any      { return mcpcore.IntProp(desc) }
func listProp(desc string) map[string]any     { return mcpcore.ListProp(desc) }
func dirProp() map[string]any {
	return mcpcore.StrProp("project root; defaults to the working directory")
}

func checkArgs(schema map[string]any, given map[string]any) error {
	return mcpcore.CheckArgs(schema, given)
}

// Every tool answers with the runner's envelope, so one schema describes them
// all; a per-tool `data` shape would be a second copy of what the runner types
// already say.
func envelopeSchema() map[string]any {
	return obj(map[string]any{
		"command": strProp("the command that ran"),
		"ok":      boolProp("false when it failed"),
		"exit":    intProp("0 ok - 1 ran and failed - 2 called wrong - 3 drift found - 4 a required tool is missing"),
		"kind":    strProp("failure class, present only on failure"),
		"error":   strProp("what went wrong, present only on failure"),
		"data":    map[string]any{"type": "object", "description": "per-command result"},
	})
}
