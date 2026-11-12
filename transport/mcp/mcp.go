// Package mcp serves rpcs as Model Context Protocol tools over stateless Streamable HTTP:
// each POST carries one JSON-RPC message, and every tool call passes the same
// Authenticator as the rpc it runs and is reported to Audit.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/redact"
	scorixconnect "github.com/tradalab/scorix/transport/connect"
	"github.com/tradalab/scorix/transport/internal/gate"
	"github.com/tradalab/scorix/transport/rest"
)

// ProtocolVersions are the MCP revisions this server answers, newest first.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26"}

// Event is one tool call, reported after it ran or was refused.
type Event struct {
	Tool      string
	Procedure string
	Principal kernel.Principal
	// Arguments are the call's arguments as JSON with SECRET fields cleared and PII masked;
	// nil when they did not decode.
	Arguments json.RawMessage
	Duration  time.Duration
	Err       error
}

// Options apply to every tool of a Mux. Auth, Timeout and Audit are required.
type Options struct {
	Auth    kernel.Authenticator
	Timeout time.Duration
	Audit   func(ctx context.Context, e Event)
	// Observer sees every tool call; nil sees none.
	Observer kernel.Observer
	// Policy answers for every tool whose Rule names a permission.
	Policy  kernel.Policy
	Name    string
	Version string
}

// Tool describes one rpc to an agent. InputSchema and OutputSchema are JSON Schema.
type Tool struct {
	Name         string
	Description  string
	Procedure    string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	ReadOnly     bool
	Idempotent   bool
	Destructive  bool
	Rule         kernel.Rule
}

type entry struct {
	Tool
	decode func(args json.RawMessage) (proto.Message, error)
	run    func(ctx context.Context, req proto.Message) (proto.Message, error)
}

type Mux struct {
	opt   Options
	tools []entry
}

func NewMux(opt Options) (*Mux, error) {
	switch {
	case opt.Auth == nil:
		return nil, errors.New("transport/mcp: Options.Auth is required")
	case opt.Timeout <= 0:
		return nil, errors.New("transport/mcp: Options.Timeout is required")
	case opt.Audit == nil:
		return nil, errors.New("transport/mcp: Options.Audit is required")
	}
	return &Mux{opt: opt}, nil
}

// Register serves fn as tool t. It panics when t.Name is registered twice, or when t.Rule
// names a permission and the Mux has no Policy.
func Register[Req, Res any, PReq interface {
	*Req
	proto.Message
}, PRes interface {
	*Res
	proto.Message
}](m *Mux, t Tool, fn func(context.Context, *Req) (*Res, error)) {
	for _, e := range m.tools {
		if e.Name == t.Name {
			panic("transport/mcp: tool " + t.Name + " registered twice")
		}
	}
	if t.Rule.Permission != "" && m.opt.Policy == nil {
		panic("transport/mcp: tool " + t.Name + " needs " + t.Rule.Permission + " and the Mux has no Policy")
	}
	m.tools = append(m.tools, entry{Tool: t,
		decode: func(args json.RawMessage) (proto.Message, error) {
			req := PReq(new(Req))
			if len(args) > 0 && string(args) != "null" {
				if err := protojson.Unmarshal(args, req); err != nil {
					return nil, &badArguments{err}
				}
			}
			return req, nil
		},
		run: func(ctx context.Context, req proto.Message) (proto.Message, error) {
			res, err := fn(ctx, (*Req)(req.(PReq)))
			if err != nil {
				return nil, err
			}
			return PRes(res), nil
		}})
}

type badArguments struct{ error }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "this MCP endpoint takes POST only", http.StatusMethodNotAllowed)
		return
	}
	raw, err := gate.ReadBody(r.Body)
	if err != nil {
		reply(w, http.StatusRequestEntityTooLarge, response{JSONRPC: "2.0", Error: &rpcError{codeInvalidRequest, "the message is too large or unreadable"}})
		return
	}
	var req request
	if err := json.Unmarshal(raw, &req); err != nil {
		reply(w, http.StatusBadRequest, response{JSONRPC: "2.0", Error: &rpcError{codeParse, "not a JSON-RPC message: " + err.Error()}})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		reply(w, http.StatusBadRequest, response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{codeInvalidRequest, "a request needs jsonrpc 2.0 and a method"}})
		return
	}
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	ctx := r.Context()
	res := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		res.Result = m.initialize(req.Params)
	case "ping":
		res.Result = map[string]any{}
	case "tools/list":
		if _, err := gate.Authenticate(ctx, m.opt.Auth, kernel.Call{Header: r.Header}); err != nil {
			refuse(w, req.ID, err)
			return
		}
		res.Result = map[string]any{"tools": m.descriptors()}
	case "tools/call":
		result, rerr, authErr := m.call(ctx, r.Header, req.Params)
		if authErr != nil {
			refuse(w, req.ID, authErr)
			return
		}
		res.Result, res.Error = result, rerr
	default:
		res.Error = &rpcError{codeMethodNotFound, "unknown method: " + req.Method}
	}
	reply(w, http.StatusOK, res)
}

func (m *Mux) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := ProtocolVersions[0]
	for _, v := range ProtocolVersions {
		if v == p.ProtocolVersion {
			version = v
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": m.opt.Name, "version": m.opt.Version},
	}
}

func (m *Mux) descriptors() []map[string]any {
	out := make([]map[string]any, 0, len(m.tools))
	for _, t := range m.tools {
		d := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.InputSchema,
			"annotations": map[string]any{"readOnlyHint": t.ReadOnly, "idempotentHint": t.Idempotent, "destructiveHint": t.Destructive, "openWorldHint": false},
		}
		if len(t.OutputSchema) > 0 {
			d["outputSchema"] = t.OutputSchema
		}
		out = append(out, d)
	}
	return out
}

func (m *Mux) call(ctx context.Context, h http.Header, params json.RawMessage) (any, *rpcError, error) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{codeInvalidParams, "tools/call params: " + err.Error()}, nil
	}
	var t *entry
	for i := range m.tools {
		if m.tools[i].Name == p.Name {
			t = &m.tools[i]
		}
	}
	if t == nil {
		return nil, &rpcError{codeInvalidParams, "unknown tool: " + p.Name}, nil
	}
	var err error
	ctx, done := kernel.Observe(ctx, m.opt.Observer, "mcp", kernel.Call{Procedure: t.Procedure, Header: h})
	defer scorixconnect.Settle(done, &err)
	ctx, cancel := context.WithTimeout(ctx, t.Rule.TimeoutOr(m.opt.Timeout))
	defer cancel()
	start := time.Now()
	authed, err := gate.Authenticate(ctx, m.opt.Auth, kernel.Call{Procedure: t.Procedure, Header: h})
	if err != nil {
		m.opt.Audit(ctx, Event{Tool: t.Name, Procedure: t.Procedure, Duration: time.Since(start), Err: err})
		return nil, nil, err
	}
	principal, _ := kernel.PrincipalFrom(authed)
	var args json.RawMessage
	var out proto.Message
	err = func() (err error) {
		defer scorixconnect.Settle(func(err error) {
			m.opt.Audit(authed, Event{Tool: t.Name, Procedure: t.Procedure, Principal: principal, Arguments: args, Duration: time.Since(start), Err: err})
		}, &err)
		req, err := t.decode(p.Arguments)
		if err != nil {
			return err
		}
		args, _ = rest.Marshal(redact.Log(req))
		if err = t.Rule.Admit(authed, principal, m.opt.Policy); err != nil {
			return err
		}
		out, err = t.run(authed, req)
		return err
	}()
	if bad, ok := errors.AsType[*badArguments](err); ok {
		return nil, &rpcError{codeInvalidParams, "arguments of " + t.Name + ": " + bad.Error()}, nil
	}
	if err != nil {
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": rest.Message(err)}}}, nil, nil
	}
	body, err := rest.Marshal(redact.Agent(out))
	if err != nil {
		return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": "the result of " + t.Name + " cannot be written as JSON: " + err.Error()}}}, nil, nil
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(body)}},
		"structuredContent": json.RawMessage(body),
	}, nil, nil
}

func refuse(w http.ResponseWriter, id json.RawMessage, err error) {
	reply(w, rest.Status(err), response{JSONRPC: "2.0", ID: id, Error: &rpcError{codeInvalidRequest, rest.Message(err)}})
}

func reply(w http.ResponseWriter, status int, res response) {
	if res.ID == nil {
		res.ID = json.RawMessage("null")
	}
	body, err := json.Marshal(res)
	if err != nil {
		status = http.StatusInternalServerError
		body, _ = json.Marshal(response{JSONRPC: "2.0", ID: res.ID, Error: &rpcError{codeInternal, "the reply cannot be written as JSON: " + err.Error()}})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}
