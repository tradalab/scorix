package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	goruntime "runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/app"
)

// AppKit refuses to run its event loop anywhere but the process's first thread,
// and init is the last moment the main goroutine is still guaranteed to be on it.
func init() { goruntime.LockOSThread() }

const (
	bootWait  = 90 * time.Second // a cold webview on a CI runner is slow to first paint
	askWait   = 20 * time.Second
	quitGrace = 5 * time.Second
)

// live is nil unless SCORIX_E2E is set, which is what makes every test skip on a
// machine with no display instead of hanging on a window that never opens.
var live *app.App

var (
	mu      sync.Mutex
	waiting = map[string]chan answer{}
	seq     atomic.Uint64
)

type answer struct {
	Tag string          `json:"tag"`
	V   json.RawMessage `json:"v"`
	Err string          `json:"err"`
}

const page = `<!doctype html>
<html>
<head><meta charset="utf-8"><title>scorix e2e</title></head>
<body>
<div id="box" style="width:120px;height:40px">e2e</div>
<script>
window.__e2e = "loaded";
scorix.invoke("e2e:report", {tag: "boot"});
</script>
</body>
</html>
`

func TestMain(m *testing.M) {
	if os.Getenv("SCORIX_E2E") == "" {
		os.Exit(m.Run())
	}
	a, err := app.New(app.Options{
		Title:      "scorix e2e",
		Width:      640,
		Height:     480,
		Identifier: "scorix-e2e",
		URL:        "scorix://app/index.html",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e: build app:", err)
		os.Exit(1)
	}
	a.Serve("scorix", fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte(page)},
		"hello.txt":  &fstest.MapFile{Data: []byte("served-by-go")},
	})
	a.Command("e2e:report", func(_ context.Context, data json.RawMessage, _ app.ChunkStream) (any, error) {
		var ans answer
		if err := json.Unmarshal(data, &ans); err != nil {
			return nil, err
		}
		deliver(ans)
		return nil, nil
	})
	a.Command("e2e:sum", func(_ context.Context, data json.RawMessage, _ app.ChunkStream) (any, error) {
		var in struct{ A, B int }
		if err := json.Unmarshal(data, &in); err != nil {
			return nil, err
		}
		return map[string]int{"sum": in.A + in.B}, nil
	})
	live = a

	booted := listen("boot")
	exit := make(chan int, 1)
	go func() {
		select {
		case <-booted:
		case <-time.After(bootWait):
			fmt.Fprintf(os.Stderr, "e2e: no message from the page in %s - the webview never ran the bridge\n", bootWait)
			os.Exit(1)
		}
		code := m.Run()
		a.Quit()
		exit <- code
	}()
	if err := a.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "e2e: run:", err)
		os.Exit(1)
	}
	select {
	case code := <-exit:
		os.Exit(code)
	// Run returns the moment the loop stops, which on the normal path is right
	// after Quit. Anything else - a closed window, a stray Quit - would otherwise
	// park here until the go test timeout and report the wrong thing.
	case <-time.After(quitGrace):
		fmt.Fprintln(os.Stderr, "e2e: the event loop stopped while tests were still running")
		os.Exit(1)
	}
}

// harness skips rather than fails: every other CI job builds this package, and
// only the e2e job has a display to run it against.
func harness(t *testing.T) *app.App {
	t.Helper()
	if live == nil {
		t.Skip("set SCORIX_E2E=1 to run against this platform's real webview")
	}
	return live
}

func listen(tag string) <-chan answer {
	ch := make(chan answer, 1)
	mu.Lock()
	waiting[tag] = ch
	mu.Unlock()
	return ch
}

func deliver(ans answer) {
	mu.Lock()
	ch := waiting[ans.Tag]
	delete(waiting, ans.Tag)
	mu.Unlock()
	if ch != nil {
		ch <- ans
	}
}

// ask evaluates expr in the page and returns what it resolved to. The answer
// comes back over the real IPC bridge, so an ask that returns at all has already
// proven the round trip; what it returns is the assertion.
func ask(t *testing.T, expr string) json.RawMessage {
	t.Helper()
	tag := "t" + strconv.FormatUint(seq.Add(1), 10)
	ch := listen(tag)
	js := fmt.Sprintf(`Promise.resolve().then(function(){return (%s)}).then(
  function(v){scorix.invoke("e2e:report",{tag:%q,v:v===undefined?null:v})},
  function(e){scorix.invoke("e2e:report",{tag:%q,err:String(e)})})`, expr, tag, tag)
	live.MainWindow().View().Eval(js)
	select {
	case ans := <-ch:
		if ans.Err != "" {
			t.Fatalf("%s\n  threw: %s", expr, ans.Err)
		}
		return ans.V
	case <-time.After(askWait):
		t.Fatalf("%s\n  no answer in %s", expr, askWait)
	}
	return nil
}

func askString(t *testing.T, expr string) string {
	t.Helper()
	var s string
	decode(t, expr, ask(t, expr), &s)
	return s
}

func askInt(t *testing.T, expr string) int {
	t.Helper()
	var n int
	decode(t, expr, ask(t, expr), &n)
	return n
}

func askBool(t *testing.T, expr string) bool {
	t.Helper()
	var b bool
	decode(t, expr, ask(t, expr), &b)
	return b
}

func decode(t *testing.T, expr string, raw json.RawMessage, into any) {
	t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s\n  answered %s, which is not a %T: %v", expr, raw, into, err)
	}
}

// eventually polls because every native setter here is a request to the display
// server, not a write: the answer is right some milliseconds after the call.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ok() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waited 5s for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
