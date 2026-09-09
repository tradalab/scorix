package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// converse feeds lines to a server and returns every reply it wrote, so a test
// can assert on what did NOT come back as well as what did.
func converse(t *testing.T, s *Server, lines ...string) []response {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got []response
	dec := json.NewDecoder(&out)
	for {
		var r response
		if err := dec.Decode(&r); err == io.EOF {
			return got
		} else if err != nil {
			t.Fatalf("reply is not JSON: %v", err)
		}
		got = append(got, r)
	}
}

// byID because replies no longer arrive in the order they were asked: a tool
// runs on its own goroutine, so a ping sent after it can answer first.
func byID(t *testing.T, got []response, id string) response {
	t.Helper()
	for _, r := range got {
		if string(r.ID) == id {
			return r
		}
	}
	t.Fatalf("no reply with id %s in %d replies", id, len(got))
	return response{}
}

func result(t *testing.T, r response) map[string]any {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("unexpected error reply: %+v", r.Error)
	}
	b, err := json.Marshal(r.Result)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInitializeAnswersWithAVersionTheClientAsked(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`)
	if len(got) != 1 {
		t.Fatalf("want 1 reply, got %d", len(got))
	}
	res := result(t, got[0])
	if res["protocolVersion"] != "2024-11-05" {
		t.Fatalf("client asked for 2024-11-05, server answered %v", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatal("a server with tools must advertise the tools capability or no client will list them")
	}
}

func TestInitializeFallsBackToAVersionWeSpeak(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := result(t, got[0])["protocolVersion"]; v != protocolVersions[0] {
		t.Fatalf("unknown version negotiated to %v, want %s", v, protocolVersions[0])
	}
}

// A reply to a notification has no id for the client to match, so it is a
// protocol error rather than harmless noise.
func TestNotificationsGetNoReply(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if len(got) != 1 {
		t.Fatalf("only the id-bearing request may be answered, got %d replies", len(got))
	}
	if string(got[0].ID) != "7" {
		t.Fatalf("answered id %s, want 7", got[0].ID)
	}
}

func TestToolsListDescribesEveryTool(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools, _ := result(t, got[0])["tools"].([]any)
	if len(tools) != len(s.tools) {
		t.Fatalf("listed %d tools, server has %d", len(tools), len(s.tools))
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		tl := raw.(map[string]any)
		name, _ := tl["name"].(string)
		if name == "" || seen[name] {
			t.Fatalf("tool name missing or duplicated: %q", name)
		}
		seen[name] = true
		if d, _ := tl["description"].(string); d == "" {
			t.Fatalf("%s has no description, so a client cannot tell when to use it", name)
		}
		schema, _ := tl["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Fatalf("%s inputSchema must be an object schema, got %v", name, schema["type"])
		}
	}
}

func TestUnknownMethodAndUnknownTool(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"nope"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"scorix_nope"}}`)
	if got[0].Error == nil || got[0].Error.Code != codeMethodNotFound {
		t.Fatalf("unknown method answered %+v", got[0])
	}
	if got[1].Error == nil {
		t.Fatalf("unknown tool answered a result: %+v", got[1].Result)
	}
}

func TestMalformedInputIsReportedAndDoesNotEndTheSession(t *testing.T) {
	s := NewServer("test")
	var out bytes.Buffer
	err := s.Serve(context.Background(), strings.NewReader("{not json\n"), &out)
	if err != nil {
		t.Fatalf("a bad message must not fail the server: %v", err)
	}
	if !strings.Contains(out.String(), `"code":-32700`) {
		t.Fatalf("no parse error reported: %s", out.String())
	}
}

func TestToolCallReturnsTheRunnerEnvelope(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scorix_doctor"}}`)
	res := result(t, got[0])
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("want one content block, got %d", len(content))
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	var env map[string]any
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("tool text is not the JSON envelope: %v\n%s", err, text)
	}
	if env["command"] != "doctor" {
		t.Fatalf("envelope names command %v, want doctor", env["command"])
	}
	if _, ok := env["ok"]; !ok {
		t.Fatal("envelope has no ok field, which is what an agent branches on")
	}
}

// A tool that fails before writing must still answer JSON: an agent that gets
// prose here has nothing to branch on.
func TestFailingToolStillAnswersAnEnvelope(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scorix_generate","arguments":{"kind":"banana"}}}`)
	res := result(t, got[0])
	if res["isError"] != true {
		t.Fatal("a rejected argument must set isError")
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	var env map[string]any
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		t.Fatalf("failure text is not JSON: %v\n%s", err, text)
	}
	if env["ok"] != false || env["error"] == nil {
		t.Fatalf("failure envelope lost ok/error: %s", text)
	}
}

func TestPanickingToolDoesNotTakeTheServerDown(t *testing.T) {
	s := NewServer("test")
	s.tools = append(s.tools, tool{
		name:   "boom",
		schema: obj(map[string]any{}),
		run:    func(context.Context, args, io.Writer) error { panic("kaboom") },
	})
	got := converse(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"boom"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if len(got) != 2 {
		t.Fatalf("server stopped after the panic: %d replies", len(got))
	}
	text := result(t, byID(t, got, "1"))["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "kaboom") {
		t.Fatalf("panic reason lost: %s", text)
	}
}

// The older revisions this server offers allow JSON-RPC batches, so answering
// one with a parse error would be advertising a protocol it does not speak.
func TestBatchRequestIsAnswered(t *testing.T) {
	s := NewServer("test")
	var out bytes.Buffer
	in := `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/initialized"},{"jsonrpc":"2.0","id":2,"method":"tools/list"}]`
	if err := s.Serve(context.Background(), strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	var batch []response
	if err := json.Unmarshal(out.Bytes(), &batch); err != nil {
		t.Fatalf("batch reply is not an array: %v\n%s", err, out.String())
	}
	if len(batch) != 2 {
		t.Fatalf("want 2 replies (the notification gets none), got %d", len(batch))
	}
}

func TestBatchOfNotificationsIsAnsweredWithSilence(t *testing.T) {
	s := NewServer("test")
	var out bytes.Buffer
	if err := s.Serve(context.Background(),
		strings.NewReader(`[{"jsonrpc":"2.0","method":"notifications/initialized"}]`), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatalf("answered a batch that asked nothing: %s", out.String())
	}
}

// A dir that is not a string used to fall back to "." and run the command in the
// wrong directory, which reads as a missing file rather than a bad call.
func TestWrongTypedArgumentIsRejectedNotIgnored(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scorix_build","arguments":{"dir":7}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"scorix_build","arguments":{"tags":"one"}}}`)
	for _, r := range got {
		if r.Error == nil {
			t.Fatalf("id %s: a wrong-typed argument was accepted", r.ID)
		}
		if !strings.Contains(r.Error.Message, "must be") {
			t.Fatalf("id %s: unhelpful message %q", r.ID, r.Error.Message)
		}
	}
}

func TestCorrectlyTypedArgumentsPass(t *testing.T) {
	if err := checkArgs(obj(map[string]any{
		"dir":   strProp(""),
		"tags":  listProp(""),
		"check": boolProp(""),
		"lines": intProp(""),
	}), map[string]any{
		"dir": ".", "tags": []any{"a"}, "check": true, "lines": float64(9), "unknown": 1,
	}); err != nil {
		t.Fatalf("well-formed arguments rejected: %v", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// A build can run for minutes. If the read loop waits for it, the client's own
// keepalive goes unanswered and it concludes the server died.
func TestASlowToolDoesNotStallTheReadLoop(t *testing.T) {
	s := NewServer("test")
	release := make(chan struct{})
	started := make(chan struct{})
	s.tools = append(s.tools, tool{
		name:   "slow",
		schema: obj(map[string]any{}),
		run: func(context.Context, args, io.Writer) error {
			close(started)
			<-release
			return nil
		},
	})

	pr, pw := io.Pipe()
	out := &syncBuffer{}
	done := make(chan struct{})
	go func() { _ = s.Serve(context.Background(), pr, out); close(done) }()

	_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"slow"}}`+"\n")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow tool never started")
	}
	// From a goroutine: an unbuffered pipe means a blocked read loop blocks the
	// writer too, and the test would hang instead of reporting.
	go func() {
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":2,"method":"ping"}`+"\n")
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), `"id":2`) {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("ping went unanswered while a tool was running: the read loop is blocked")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(release)
	pw.Close()
	<-done
}

// Running the read loop concurrently must not let two runner calls overlap: the
// runner prints through package-level os.Stdout and shares state per project.
func TestToolBodiesStillRunOneAtATime(t *testing.T) {
	s := NewServer("test")
	var live, peak atomic.Int32
	s.tools = append(s.tools, tool{
		name:   "slow",
		schema: obj(map[string]any{}),
		run: func(context.Context, args, io.Writer) error {
			n := live.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(150 * time.Millisecond)
			live.Add(-1)
			return nil
		},
	})

	var in strings.Builder
	for i := 1; i <= 4; i++ {
		fmt.Fprintf(&in, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"slow"}}`+"\n", i)
	}
	got := converse(t, s, in.String())
	if len(got) != 4 {
		t.Fatalf("want 4 replies, got %d", len(got))
	}
	if peak.Load() != 1 {
		t.Fatalf("%d tool bodies ran at once; the runner is not reentrant", peak.Load())
	}
}

// Order is a guarantee the serial read loop used to give for free. dev_stop
// executing before the dev_start ahead of it is a wrong answer, not a slow one.
func TestToolBodiesRunInArrivalOrder(t *testing.T) {
	s := NewServer("test")
	var mu sync.Mutex
	var order []string
	s.tools = append(s.tools, tool{
		name:   "note",
		schema: obj(map[string]any{"tag": strProp("")}),
		run: func(_ context.Context, a args, _ io.Writer) error {
			mu.Lock()
			order = append(order, a.str("tag", "?"))
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			return nil
		},
	})

	var in strings.Builder
	want := []string{"a", "b", "c", "d", "e", "f"}
	for i, tag := range want {
		fmt.Fprintf(&in, `{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"note","arguments":{"tag":%q}}}`+"\n", i+1, tag)
	}
	if got := converse(t, s, in.String()); len(got) != len(want) {
		t.Fatalf("want %d replies, got %d", len(want), len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(order, "") != strings.Join(want, "") {
		t.Fatalf("ran in order %v, want %v", order, want)
	}
}

// The shapes a real client sends that a hand-rolled transport gets wrong. Each of
// these was found by firing it at the built binary; they live here so the next
// change to the read loop cannot quietly lose one.
func TestTransportShapesRealClientsSend(t *testing.T) {
	big := strings.Repeat("x", 200_000)
	cases := []struct {
		name    string
		line    string
		wantIDs []string
	}{
		{"crlf line ending", `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\r", []string{"1"}},
		{"string id", `{"jsonrpc":"2.0","id":"abc","method":"ping"}`, []string{`"abc"`}},
		{"blank line between messages", "", nil},
		{"200KB argument", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"scorix_generate","arguments":{"kind":"proto","file":"` + big + `"}}}`, []string{"3"}},
		{"tools/call with no params", `{"jsonrpc":"2.0","id":4,"method":"tools/call"}`, []string{"4"}},
		{"call with no id is a notification", `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"scorix_doctor"}}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := converse(t, NewServer("test"), c.line)
			if len(got) != len(c.wantIDs) {
				t.Fatalf("want %d replies, got %d: %+v", len(c.wantIDs), len(got), got)
			}
			for i, id := range c.wantIDs {
				if string(got[i].ID) != id {
					t.Fatalf("reply %d has id %s, want %s", i, got[i].ID, id)
				}
			}
		})
	}
}

// A tool that ignores its context keeps running, but the caller must still be
// told; a client that cancels and hears nothing has no way back.
func TestCancelNotificationStopsAToolThatWatchesItsContext(t *testing.T) {
	s := NewServer("test")
	started := make(chan struct{})
	s.tools = append(s.tools, tool{
		name:   "patient",
		schema: obj(map[string]any{}),
		run: func(ctx context.Context, _ args, _ io.Writer) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})

	pr, pw := io.Pipe()
	out := &syncBuffer{}
	done := make(chan struct{})
	go func() { _ = s.Serve(context.Background(), pr, out); close(done) }()

	go func() {
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"patient"}}`+"\n")
		<-started
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":9}}`+"\n")
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), `"id":9`) {
		if time.Now().After(deadline) {
			pw.Close()
			t.Fatal("the cancelled call never answered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pw.Close()
	<-done
	if !strings.Contains(out.String(), "context canceled") {
		t.Fatalf("the reply does not say it was cancelled: %s", out.String())
	}
}

func TestToolDeadlineEndsAWedgedCall(t *testing.T) {
	s := NewServer("test")
	s.tools = append(s.tools, tool{
		name:    "wedged",
		schema:  obj(map[string]any{}),
		timeout: 150 * time.Millisecond,
		run: func(ctx context.Context, _ args, _ io.Writer) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wedged"}}`)
	text := result(t, got[0])["content"].([]any)[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "deadline exceeded") {
		t.Fatalf("no deadline in the reply: %s", text)
	}
}

// The whole point of a direct tool: it answers while the turn chain is blocked.
func TestStatusAnswersWhileAToolIsStuck(t *testing.T) {
	s := NewServer("test")
	release := make(chan struct{})
	started := make(chan struct{})
	s.tools = append(s.tools, tool{
		name:   "stuck",
		schema: obj(map[string]any{}),
		run: func(context.Context, args, io.Writer) error {
			close(started)
			<-release
			return nil
		},
	})

	pr, pw := io.Pipe()
	out := &syncBuffer{}
	done := make(chan struct{})
	go func() { _ = s.Serve(context.Background(), pr, out); close(done) }()

	go func() {
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"stuck"}}`+"\n")
		<-started
		_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"scorix_status"}}`+"\n")
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), `"id":2`) {
		if time.Now().After(deadline) {
			close(release)
			pw.Close()
			t.Fatal("scorix_status queued behind the stuck tool, so it is useless exactly when it is needed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(out.String(), `"tool":"stuck"`) {
		t.Fatalf("status did not name what is running: %s", out.String())
	}
	close(release)
	pw.Close()
	<-done
}

// A client that reads only structuredContent must get the same facts as one that
// parses the text block by hand.
func TestToolResultCarriesTheEnvelopeParsed(t *testing.T) {
	s := NewServer("test")
	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"scorix_doctor"}}`)
	res := result(t, got[0])
	structured, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", res)
	}
	if structured["command"] != "doctor" {
		t.Fatalf("structuredContent names %v", structured["command"])
	}
	text := res["content"].([]any)[0].(map[string]any)["text"].(string)
	var fromText map[string]any
	if err := json.Unmarshal([]byte(text), &fromText); err != nil {
		t.Fatal(err)
	}
	if fromText["ok"] != structured["ok"] || fromText["exit"] != structured["exit"] {
		t.Fatal("the text block and structuredContent disagree")
	}
}

// Spec rule: progress goes out only to a client that asked for it with a token.
// Sending it unasked is noise a client is entitled to reject.
func TestProgressOnlyWhenTheClientAsked(t *testing.T) {
	for _, withToken := range []bool{false, true} {
		name := "no token"
		params := `{"name":"chatty"}`
		if withToken {
			name = "with token"
			params = `{"name":"chatty","_meta":{"progressToken":"tok-1"}}`
		}
		t.Run(name, func(t *testing.T) {
			s := NewServer("test")
			runnerOut, runnerIn := io.Pipe()
			s.WatchOutput(runnerOut, nil)
			s.tools = append(s.tools, tool{
				name:   "chatty",
				schema: obj(map[string]any{}),
				run: func(_ context.Context, _ args, out io.Writer) error {
					_, _ = io.WriteString(runnerIn, "==> step one\n")
					time.Sleep(150 * time.Millisecond)
					_, _ = io.WriteString(out, `{"command":"chatty","ok":true,"exit":0}`)
					return nil
				},
			})

			pr, pw := io.Pipe()
			got := &syncBuffer{}
			done := make(chan struct{})
			go func() { _ = s.Serve(context.Background(), pr, got); close(done) }()
			_, _ = io.WriteString(pw, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+params+"}\n")

			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(got.String(), `"id":1`) {
				if time.Now().After(deadline) {
					pw.Close()
					t.Fatal("the call never answered")
				}
				time.Sleep(20 * time.Millisecond)
			}
			pw.Close()
			<-done

			sawProgress := strings.Contains(got.String(), "notifications/progress")
			if sawProgress != withToken {
				t.Fatalf("progress sent = %v, client asked = %v\n%s", sawProgress, withToken, got.String())
			}
			if withToken && !strings.Contains(got.String(), "step one") {
				t.Fatalf("progress carried no message: %s", got.String())
			}
		})
	}
}

// TestMCPHelperProcess is a stand-in for the long child processes the runner
// spawns (go build, pnpm, wix). It exists only to be killed.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("MCP_HELPER") == "" {
		return
	}
	time.Sleep(2 * time.Minute)
}

// D1 asked whether tools should shell out so a wedged one can be killed. Every
// subprocess the runner starts already goes through exec.CommandContext, so the
// deadline reaches them; this pins that down instead of trusting the grep.
func TestDeadlineKillsARealChildProcess(t *testing.T) {
	s := NewServer("test")
	var elapsed time.Duration
	s.tools = append(s.tools, tool{
		name:    "spawner",
		schema:  obj(map[string]any{}),
		timeout: 300 * time.Millisecond,
		run: func(ctx context.Context, _ args, _ io.Writer) error {
			start := time.Now()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMCPHelperProcess$")
			cmd.Env = append(os.Environ(), "MCP_HELPER=1")
			err := cmd.Run()
			elapsed = time.Since(start)
			return err
		},
	})

	got := converse(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"spawner"}}`)
	if len(got) != 1 {
		t.Fatalf("want 1 reply, got %d", len(got))
	}
	if elapsed > 30*time.Second {
		t.Fatalf("the child outlived the deadline by %s: shelling out would be the only fix", elapsed)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("returned in %s, before the deadline could have fired", elapsed)
	}
}
