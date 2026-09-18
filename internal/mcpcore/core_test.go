package mcpcore

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func served(t *testing.T, s *Server) (send func(v any), next func() map[string]any) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = s.Serve(context.Background(), inR, outW); _ = outW.Close() }()
	t.Cleanup(func() { _ = inW.Close() })
	lines := bufio.NewReader(outR)
	send = func(v any) {
		b, _ := json.Marshal(v)
		go func() { _, _ = inW.Write(append(b, '\n')) }()
	}
	next = func() map[string]any {
		t.Helper()
		ch := make(chan []byte, 1)
		go func() { line, _ := lines.ReadBytes('\n'); ch <- line }()
		select {
		case line := <-ch:
			var m map[string]any
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatalf("not JSON: %q", line)
			}
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("no message in 5s")
		}
		return nil
	}
	return send, next
}

func TestProgressAndClientNameReachTheToolThatIsRunning(t *testing.T) {
	var client string
	var progressed, silent bool
	s := New(Options{Tools: []Tool{
		{Name: "scan", Run: func(ctx context.Context, _ Args, out io.Writer) error {
			client = ClientName(ctx)
			progressed = Progress(ctx, "halfway")
			_, err := io.WriteString(out, `{"done":true}`)
			return err
		}},
		{Name: "quiet", Run: func(ctx context.Context, _ Args, out io.Writer) error {
			silent = !Progress(ctx, "nobody asked")
			_, err := io.WriteString(out, `{}`)
			return err
		}},
	}})
	send, next := served(t, s)
	send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]any{"name": "claude-ai"}}})
	next()
	send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "scan", "_meta": map[string]any{"progressToken": "tok"}}})
	note := next()
	if note["method"] != "notifications/progress" {
		t.Fatalf("first message = %v, want the progress notification before the result", note)
	}
	params := note["params"].(map[string]any)
	if params["progressToken"] != "tok" || params["message"] != "halfway" {
		t.Errorf("progress = %v", params)
	}
	if res := next(); res["id"] != 2.0 {
		t.Fatalf("then %v, want the result", res)
	}
	if !progressed || client != "claude-ai" {
		t.Errorf("Progress = %v, ClientName = %q", progressed, client)
	}
	send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "quiet"}})
	if res := next(); res["id"] != 3.0 || !silent {
		t.Errorf("a call without a progress token: %v, Progress reported sent = %v", res, !silent)
	}
}

func TestOutputSchemaIsPerToolAndErrorsCarryNoStructuredContent(t *testing.T) {
	out := map[string]any{"type": "object", "properties": map[string]any{"done": map[string]any{"type": "boolean"}}}
	s := New(Options{Tools: []Tool{{Name: "scan", OutputSchema: out}}})
	if got := s.toolDescriptors()[0]["outputSchema"]; got == nil {
		t.Fatal("the tool's own outputSchema is not in its descriptor")
	}
	failing := Tool{Name: "scan", OutputSchema: out, Run: func(_ context.Context, _ Args, w io.Writer) error {
		_, _ = io.WriteString(w, `{"error":"boom","code":"internal"}`)
		return io.ErrUnexpectedEOF
	}}
	res := s.runCall(context.Background(), failing, nil)
	if _, has := res["structuredContent"]; has || res["isError"] != true {
		t.Errorf("failed call result = %v, want isError and no structuredContent", res)
	}
}

func TestAnnotationsReachTheDescriptorOnlyWhenSet(t *testing.T) {
	s := New(Options{Tools: []Tool{
		{Name: "wipe", Annotations: map[string]any{"destructiveHint": true}},
		{Name: "list"},
	}})
	ds := s.toolDescriptors()
	if got := ds[0]["annotations"]; got == nil || got.(map[string]any)["destructiveHint"] != true {
		t.Errorf("wipe descriptor = %v, want destructiveHint", ds[0])
	}
	if _, ok := ds[1]["annotations"]; ok {
		t.Errorf("list descriptor carries annotations it never set: %v", ds[1])
	}
}

func TestStructuredContentIsOnlyEverAnObject(t *testing.T) {
	for text, wantStructured := range map[string]bool{
		`{"keys":["a"]}`: true,
		`["a","b"]`:      false,
		`"ok"`:           false,
		`null`:           false,
	} {
		s := New(Options{})
		tool := Tool{Name: "t", Run: func(_ context.Context, _ Args, out io.Writer) error {
			_, err := io.WriteString(out, text)
			return err
		}}
		res := s.runCall(context.Background(), tool, nil)
		if _, got := res["structuredContent"]; got != wantStructured {
			t.Errorf("%s: structuredContent present = %v, want %v", text, got, wantStructured)
		}
	}
}
