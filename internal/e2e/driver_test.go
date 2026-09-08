package e2e

import (
	"strconv"
	"testing"
	"time"

	"github.com/tradalab/scorix/window"
)

func TestPageLoadsOverTheCustomScheme(t *testing.T) {
	harness(t)
	if got := askString(t, "location.protocol"); got != "scorix:" {
		t.Fatalf("page origin is %q: the document did not come from the Go scheme handler", got)
	}
	if got := askString(t, "window.__e2e"); got != "loaded" {
		t.Fatalf("window.__e2e is %q: the page's own script never ran", got)
	}
	if got := askString(t, "scorix.mode"); got != "app" {
		t.Fatalf("bridge reports mode %q, want app", got)
	}
}

// The first document can arrive through a navigation the driver special-cases;
// only a fetch proves the scheme answers ordinary requests too.
func TestSchemeAnswersLaterRequests(t *testing.T) {
	harness(t)
	got := askString(t, `fetch("scorix://app/hello.txt").then(function(r){return r.text()})`)
	if got != "served-by-go" {
		t.Fatalf("fetch over the custom scheme returned %q", got)
	}
}

func TestCommandRoundTrip(t *testing.T) {
	harness(t)
	got := askInt(t, `scorix.invoke("e2e:sum",{a:20,b:22}).then(function(r){return r.sum})`)
	if got != 42 {
		t.Fatalf("20+22 came back as %d", got)
	}
}

func TestUnknownCommandRejectsWithItsCode(t *testing.T) {
	harness(t)
	got := askString(t, `scorix.invoke("e2e:nope").then(
		function(){return "resolved"},
		function(e){return e.name + ":" + (e.code || "-")})`)
	if got != "ScorixError:not_found" {
		t.Fatalf("a missing handler surfaced as %q", got)
	}
}

func TestEventFromGoReachesThePage(t *testing.T) {
	a := harness(t)
	nonce := strconv.FormatInt(time.Now().UnixNano(), 36)
	// Arm through ask, not Eval: Emit must not race the listener, and only an
	// answer proves the JS already ran.
	armed := askString(t, `(window.__ping = new Promise(function(res){
		scorix.on("e2e:ping", function(d){res(d.nonce)})}), "armed")`)
	if armed != "armed" {
		t.Fatalf("arming the listener answered %q", armed)
	}
	a.Emit("e2e:ping", map[string]string{"nonce": nonce})
	if got := askString(t, "window.__ping"); got != nonce {
		t.Fatalf("event payload arrived as %q, want %q", got, nonce)
	}
}

// Everything above would also pass with a JS context and no window on screen; a
// laid-out viewport is what says the surface is real.
func TestWebviewHasALaidOutViewport(t *testing.T) {
	harness(t)
	if w := askInt(t, "window.innerWidth"); w <= 0 {
		t.Fatalf("innerWidth is %d: the webview never got a surface", w)
	}
	if h := askInt(t, `document.getElementById("box").getBoundingClientRect().height|0`); h <= 0 {
		t.Fatalf("the div measured %dpx tall: the page loaded but never laid out", h)
	}
}

func TestWindowSizeRoundTripsThroughTheDriver(t *testing.T) {
	a := harness(t)
	w := a.MainWindow()
	ow, oh := w.Size()
	t.Cleanup(func() { w.SetSize(ow, oh) })

	w.SetSize(720, 540)
	eventually(t, "SetSize(720,540) to read back from the driver", func() bool {
		gw, gh := w.Size()
		return gw == 720 && gh == 540
	})
	// Size() can echo a request the surface never honored (GTK returns the pending
	// resize), so the viewport is the second opinion; chrome costs at most ~120px.
	iw, ih := askInt(t, "window.innerWidth"), askInt(t, "window.innerHeight")
	if iw > 720 || iw <= 600 || ih > 540 || ih <= 420 {
		t.Fatalf("the driver reports 720x540 but the page sees %dx%d", iw, ih)
	}
}

// Placement is a request a window manager may adjust, so the assertion is that
// the move reached the display server at all - not that it landed exactly.
func TestWindowPositionRoundTripsThroughTheDriver(t *testing.T) {
	a := harness(t)
	w := a.MainWindow()
	ox, oy := w.Position()
	if near(ox, 80) && near(oy, 60) {
		t.Skipf("window already opens at %d,%d, so moving it there proves nothing", ox, oy)
	}
	t.Cleanup(func() { w.SetPosition(ox, oy) })

	w.SetPosition(80, 60)
	eventually(t, "SetPosition(80,60) to read back near where it was sent", func() bool {
		x, y := w.Position()
		return near(x, 80) && near(y, 60)
	})
}

func TestMaximizeReachesTheWindowManager(t *testing.T) {
	a := harness(t)
	w := a.MainWindow()
	t.Cleanup(func() { w.Restore(); w.Unmaximize() })

	w.Maximize()
	eventually(t, "the window to report itself maximized", func() bool {
		return w.State() == window.StateMaximized
	})
	w.Unmaximize()
	eventually(t, "the window to report itself normal again", func() bool {
		return w.State() == window.StateNormal
	})
}

func near(got, want int) bool { return got-want < 100 && want-got < 100 }
