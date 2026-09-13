// Package mcpcore is the MCP transport and dispatch, tool-agnostic: the CLI
// server and the in-app dev server both sit on it.
//
// It imports nothing from internal/cli because a shipped app links this package,
// and the CLI runner coming along would end the single-exe claim - so the
// envelope for a panicking tool and the shared output schema are injected.
package mcpcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// Versions this server speaks, newest first. A client asking for one of these
// gets it back; anything else is answered with the newest and left to decide.
var ProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Only bounds the pathological case: a client that closed its pipe has left.
const shutdownGrace = 60 * time.Second

// A backstop, not a budget: a status tool is what a caller reads while something
// is still legitimately running. Tools that drive a real build set their own.
const DefaultToolTimeout = 5 * time.Minute

const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Exported for a tool table that asserts on them: the JSON-RPC codes are part of
// what a client sees, so a second copy in another package would be a second
// protocol.
const (
	CodeParse          = codeParse
	CodeInvalidRequest = codeInvalidRequest
	CodeMethodNotFound = codeMethodNotFound
	CodeInvalidParams  = codeInvalidParams
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
	Run         func(ctx context.Context, a Args, out io.Writer) error
	Timeout     time.Duration // zero uses DefaultToolTimeout
	Direct      bool          // answers on the read loop, takes no turn
}

type Options struct {
	// Name and Version go out in initialize's serverInfo.
	Name    string
	Version string
	Tools   []Tool
	// Fallback writes the envelope for a tool that panicked before writing its
	// own; without it that call answers empty, which reads as success.
	Fallback func(command string, err error) string
	// OutputSchema describes what every tool answers with; nil omits the key.
	OutputSchema func() map[string]any
}

type Server struct {
	name    string
	version string
	tools   []Tool

	fallback     func(string, error) string
	outputSchema func() map[string]any

	// Tool bodies run one at a time AND in arrival order: a runner is usually not
	// reentrant, and a stop overtaking the start ahead of it is a wrong answer
	// rather than a slow one. A plain mutex gives the first, not the second.
	turnMu   sync.Mutex
	lastTurn chan struct{}
	// A reply can come from a tool goroutine or from the read loop.
	writeMu sync.Mutex

	// What is running right now, so notifications/cancelled has something to
	// cancel and a status tool has something to report.
	callMu sync.Mutex
	calls  map[string]*liveCall

	progressIn  io.Reader
	progressTee io.Writer
}

type liveCall struct {
	tool    string
	started time.Time
	cancel  context.CancelFunc
	token   json.RawMessage // only set when the client asked for progress
	seq     int
}

func New(opt Options) *Server {
	if opt.Name == "" {
		opt.Name = "scorix"
	}
	return &Server{
		name:         opt.Name,
		version:      opt.Version,
		tools:        opt.Tools,
		fallback:     opt.Fallback,
		outputSchema: opt.OutputSchema,
		calls:        map[string]*liveCall{},
	}
}

// Running reports the live calls, for a status tool to answer with.
func (s *Server) Running() []map[string]any {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	out := make([]map[string]any, 0, len(s.calls))
	for id, c := range s.calls {
		out = append(out, map[string]any{
			"request_id": id,
			"tool":       c.tool,
			"elapsed":    time.Since(c.started).Round(time.Second).String(),
		})
	}
	return out
}

// WatchOutput forwards what a tool prints as progress notifications, so nothing
// in the tool had to change. Attributing a line is safe because the turn chain
// means at most one tool is running.
func (s *Server) WatchOutput(r io.Reader, tee io.Writer) {
	s.progressIn, s.progressTee = r, tee
}

// One message per line, unmarshalled on its own: a json.Decoder cannot resync
// past a syntax error, so one malformed line would spin forever. Reader, not
// Scanner, because a large tools/call exceeds Scanner's token limit.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	var inflight sync.WaitGroup
	defer func() {
		// Work asked for BEFORE the client left still finishes. These tools write
		// to the user's disk, so a half-done `go build` costs more than a wasted
		// minute; the grace is there so a wedged tool cannot hold the process.
		done := make(chan struct{})
		go func() { inflight.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(shutdownGrace):
			cancel()
			<-done
		}
		cancel()
	}()

	r := bufio.NewReader(in)
	enc := json.NewEncoder(out)
	if s.progressIn != nil {
		go s.pumpOutput(enc)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		line, readErr := r.ReadString('\n')
		if err := s.serveLine(ctx, line, enc, &inflight); err != nil {
			return err
		}
		if readErr != nil {
			return nil // EOF, or a transport that went away
		}
	}
}

func (s *Server) serveLine(ctx context.Context, line string, enc *json.Encoder, inflight *sync.WaitGroup) error {
	raw := bytes.TrimSpace([]byte(line))
	if len(raw) == 0 {
		return nil // a blank line, and the last read before EOF
	}
	if raw[0] == '[' {
		return s.serveBatch(ctx, raw, enc)
	}
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return s.write(enc, parseError(err))
	}
	if req.Method == "tools/call" {
		return s.dispatchCall(ctx, req, enc, inflight)
	}
	resp, reply := s.handle(req)
	if !reply {
		return nil
	}
	return s.write(enc, resp)
}

// Batches exist in the 2025-03-26 and 2024-11-05 revisions this server offers;
// rejecting them while advertising those versions is claiming a protocol we do
// not speak. A batch of notifications alone is answered with nothing, per spec.
func (s *Server) serveBatch(ctx context.Context, raw []byte, enc *json.Encoder) error {
	var reqs []Request
	if err := json.Unmarshal(raw, &reqs); err != nil {
		return s.write(enc, parseError(err))
	}
	if len(reqs) == 0 {
		return s.write(enc, Response{
			JSONRPC: "2.0",
			ID:      json.RawMessage("null"),
			Error:   &RPCError{Code: codeInvalidRequest, Message: "empty batch"},
		})
	}
	out := make([]Response, 0, len(reqs))
	for _, req := range reqs {
		// Tool calls run inline here, unlike the single-message path: a batch
		// answers with one array, and that cannot be assembled from replies
		// wandering in later.
		if req.Method == "tools/call" {
			if resp, reply := s.callResponse(ctx, req); reply {
				out = append(out, resp)
			}
			continue
		}
		if resp, reply := s.handle(req); reply {
			out = append(out, resp)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return s.write(enc, out)
}

func (s *Server) write(enc *json.Encoder, v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return enc.Encode(v)
}

func parseError(err error) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      json.RawMessage("null"),
		Error:   &RPCError{Code: codeParse, Message: err.Error()},
	}
}

// handle returns reply=false for notifications, which carry no id and must go
// unanswered: replying to one is a protocol error the client cannot match up.
func (s *Server) handle(req Request) (Response, bool) {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	notification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		resp.Result = s.initialize(req.Params)
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": s.toolDescriptors()}
	case "notifications/cancelled":
		s.cancelCall(req.Params)
	default:
		if notification {
			return resp, false // notifications/initialized and friends
		}
		resp.Error = &RPCError{Code: codeMethodNotFound, Message: "unknown method: " + req.Method}
	}
	return resp, !notification
}

// dispatchCall answers an argument error inline but runs the tool on its own
// goroutine, so a build taking minutes does not stop the loop from reading: a
// client whose keepalive goes unanswered concludes the server died.
func (s *Server) dispatchCall(ctx context.Context, req Request, enc *json.Encoder, inflight *sync.WaitGroup) error {
	notification := len(req.ID) == 0
	t, arguments, token, err := s.resolveCallMeta(req.Params)
	if err != nil {
		if notification {
			return nil
		}
		return s.write(enc, Response{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &RPCError{Code: codeInvalidParams, Message: err.Error()},
		})
	}
	if notification {
		return nil
	}
	// A tool marked Direct answers on the read loop and takes no slot, so it
	// still works while something ahead of it is wedged - which is exactly when
	// the caller needs to ask what is going on.
	if t.Direct {
		return s.write(enc, Response{JSONRPC: "2.0", ID: req.ID, Result: s.runCall(ctx, t, arguments)})
	}
	// The slot is taken here, on the read loop, because that is the only place
	// arrival order still exists; goroutines start in whatever order they like.
	prev, done := s.takeTurn()
	inflight.Add(1)
	go func() {
		defer inflight.Done()
		defer done()
		if prev != nil {
			<-prev
		}
		callCtx, release := s.beginCall(ctx, req.ID, t, token)
		defer release()
		_ = s.write(enc, Response{JSONRPC: "2.0", ID: req.ID, Result: s.runCall(callCtx, t, arguments)})
	}()
	return nil
}

// Registered while it runs, so notifications/cancelled and a status tool can
// both find it.
func (s *Server) beginCall(ctx context.Context, id json.RawMessage, t Tool, token json.RawMessage) (context.Context, func()) {
	limit := t.Timeout
	if limit == 0 {
		limit = DefaultToolTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	key := string(id)
	s.callMu.Lock()
	s.calls[key] = &liveCall{tool: t.Name, started: time.Now(), cancel: cancel, token: token}
	s.callMu.Unlock()
	return callCtx, func() {
		s.callMu.Lock()
		delete(s.calls, key)
		s.callMu.Unlock()
		cancel()
	}
}

// A tool that never looks at its context keeps running; a runner's subprocesses
// do stop.
func (s *Server) cancelCall(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	s.callMu.Lock()
	call := s.calls[string(p.RequestID)]
	s.callMu.Unlock()
	if call != nil {
		call.cancel()
	}
}

func (s *Server) callResponse(ctx context.Context, req Request) (Response, bool) {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	if len(req.ID) == 0 {
		return resp, false
	}
	t, arguments, _, err := s.resolveCallMeta(req.Params)
	if err != nil {
		resp.Error = &RPCError{Code: codeInvalidParams, Message: err.Error()}
		return resp, true
	}
	prev, done := s.takeTurn()
	defer done()
	if prev != nil {
		<-prev
	}
	resp.Result = s.runCall(ctx, t, arguments)
	return resp, true
}

func (s *Server) resolveCallMeta(params json.RawMessage) (Tool, map[string]any, json.RawMessage, error) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
		Meta      struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return Tool{}, nil, nil, err
	}
	t, ok := s.tool(p.Name)
	if !ok {
		return Tool{}, nil, nil, fmt.Errorf("unknown tool: %q", p.Name)
	}
	if err := CheckArgs(t.Schema, p.Arguments); err != nil {
		return Tool{}, nil, nil, err
	}
	return t, p.Arguments, p.Meta.ProgressToken, nil
}

// Slots are handed out in call order and each waits for the one before it, so
// the chain both serializes and orders.
func (s *Server) takeTurn() (prev <-chan struct{}, done func()) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()
	prev = s.lastTurn
	mine := make(chan struct{})
	s.lastTurn = mine
	return prev, func() { close(mine) }
}

func (s *Server) runCall(ctx context.Context, t Tool, arguments map[string]any) map[string]any {
	text, failed := s.invoke(ctx, t, arguments)
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": failed,
	}
	// The envelope goes back parsed as well as as text: a client that only
	// reads content would otherwise hand the model a JSON string to parse by
	// hand, which is the one thing this whole layer exists to avoid.
	var structured any
	if json.Unmarshal([]byte(text), &structured) == nil {
		res["structuredContent"] = structured
	}
	return res
}

// invoke returns the tool's JSON document and whether it failed. The document is
// the tool's own envelope, so a caller branches on the same fields it would get
// from the command line - this layer is transport, not translation.
func (s *Server) invoke(ctx context.Context, t Tool, raw map[string]any) (text string, failed bool) {
	var buf bytes.Buffer
	err := guarded(ctx, t, Args(raw), &buf)
	if buf.Len() == 0 {
		return s.fallbackEnvelope(t.Name, err), true
	}
	return buf.String(), err != nil
}

func (s *Server) fallbackEnvelope(command string, err error) string {
	if err == nil {
		err = fmt.Errorf("produced no result")
	}
	if s.fallback != nil {
		return s.fallback(command, err)
	}
	b, _ := json.Marshal(map[string]any{"command": command, "ok": false, "error": err.Error()})
	return string(b)
}

// A panic in a tool would take the whole server down with it, and the client
// would see the pipe close rather than which tool broke.
func guarded(ctx context.Context, t Tool, a Args, out io.Writer) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return t.Run(ctx, a, out)
}

func (s *Server) initialize(params json.RawMessage) map[string]any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &p)
	version := ProtocolVersions[0]
	for _, v := range ProtocolVersions {
		if v == p.ProtocolVersion {
			version = v
			break
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo":      map[string]any{"name": s.name, "version": s.version},
	}
}

func (s *Server) toolDescriptors() []map[string]any {
	out := make([]map[string]any, 0, len(s.tools))
	for _, t := range s.tools {
		d := map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"inputSchema": t.Schema,
		}
		if s.outputSchema != nil {
			d["outputSchema"] = s.outputSchema()
		}
		out = append(out, d)
	}
	return out
}

func (s *Server) tool(name string) (Tool, bool) {
	for _, t := range s.tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

func (s *Server) pumpOutput(enc *json.Encoder) {
	sc := bufio.NewScanner(s.progressIn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if s.progressTee != nil {
			// Drained and echoed no matter what: a full pipe would block the very
			// tool call we are reporting on.
			_, _ = io.WriteString(s.progressTee, line+"\n")
		}
		s.notifyProgress(enc, line)
	}
}

func (s *Server) notifyProgress(enc *json.Encoder, message string) {
	s.callMu.Lock()
	var call *liveCall
	for _, c := range s.calls {
		if len(c.token) > 0 {
			call = c
			break
		}
	}
	if call == nil {
		s.callMu.Unlock()
		return
	}
	call.seq++
	params := map[string]any{
		"progressToken": call.token,
		"progress":      call.seq,
		"message":       message,
	}
	s.callMu.Unlock()

	_ = s.write(enc, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/progress",
		"params":  params,
	})
}
