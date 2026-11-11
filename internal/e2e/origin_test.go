package e2e

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/app"
	"github.com/tradalab/scorix/window"
)

const homePage = `<!doctype html><html><body>home<script>
scorix.invoke("e2e:report", {tag: "nav-home"});
</script></body></html>`

var external = make(chan string, 4)

const linkPage = `<!doctype html><html><body style="margin:0">
<a id="out" href="about:blank" style="position:fixed;inset:0;display:block;background:#eee">out</a>
<script>
document.addEventListener("click", function(){ scorix.invoke("e2e:report", {tag: "nav-clicked"}) }, true);
scorix.invoke("e2e:report", {tag: "nav-link"});
</script></body></html>`

const awayPage = `<!doctype html><html><body>away<script>
scorix.invoke("e2e:report", {tag: "nav-away"});
setTimeout(function(){
  var to = decodeURIComponent(location.hash.slice(1));
  scorix.invoke("e2e:report", {tag: "nav-leaving " + to});
  location.href = to;
}, 300);
</script></body></html>`

type site struct {
	*httptest.Server
	hits chan string
}

var framed site

const otherPage = `<!doctype html><html><body>other<script>
scorix.invoke("e2e:report", {tag: "nav-other"});
</script></body></html>`

type watchedFS struct {
	fs.FS
	opened chan string
}

func (w watchedFS) Open(name string) (fs.File, error) {
	select {
	case w.opened <- name:
	default:
	}
	return w.FS.Open(name)
}

var foreignScheme = watchedFS{
	FS:     fstest.MapFS{"nav.html": &fstest.MapFile{Data: []byte(otherPage)}},
	opened: make(chan string, 4),
}

func foreignHTTP(t *testing.T) site {
	t.Helper()
	s := newSite()
	t.Cleanup(s.Close)
	return s
}

func newSite() site {
	s := site{hits: make(chan string, 16)}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.hits <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><p>foreign</p><img src="/rendered` + r.URL.Path + `">`))
	}))
	return s
}

func (s site) saw(path string, within time.Duration) bool {
	deadline := time.After(within)
	for {
		select {
		case got := <-s.hits:
			if got == path {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func drain() {
	for {
		select {
		case <-external:
		default:
			return
		}
	}
}

func open(t *testing.T, a *app.App, id, page, tag string) *app.AppWindow {
	t.Helper()
	loaded := listen(tag)
	aw, err := a.OpenWindow(window.Options{ID: window.ID(id), Title: id, Width: 320, Height: 240, URL: "scorix://app/" + page})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(aw.Close)
	select {
	case <-loaded:
	case <-time.After(askWait):
		t.Fatalf("%s never reached Go: the control half of this test is broken", page)
	}
	return aw
}

func stillHome(t *testing.T, aw *app.AppWindow, tag string) {
	t.Helper()
	back := listen(tag)
	aw.View().Eval(`scorix.invoke("e2e:report", {tag: "` + tag + `"})`)
	select {
	case <-back:
	case <-time.After(askWait):
		t.Fatal("the app's page no longer answers: the window left it")
	}
}

func TestAScriptedNavigationOutOfTheOriginNeverRendersInTheWindow(t *testing.T) {
	a := harness(t)
	drain()
	foreign := foreignHTTP(t)
	leaving := listen("nav-leaving " + foreign.URL + "/page")
	aw := open(t, a, "nav-script", "nav-away.html#"+url.QueryEscape(foreign.URL+"/page"), "nav-away")
	select {
	case <-leaving:
	case <-time.After(askWait):
		t.Fatal("the page never tried to leave, so nothing here was kept out")
	}
	if foreign.saw("/rendered/page", 4*time.Second) {
		t.Fatal("a foreign page rendered in the app's window")
	}
	stillHome(t, aw, "nav-script-home")
	select {
	case u := <-external:
		t.Fatalf("a scripted navigation was opened outside: %s", u)
	default:
	}
}

func TestAPageNavigatedToAnotherSchemeLosesTheBridge(t *testing.T) {
	a := harness(t)
	aw := open(t, a, "nav-scheme", "nav-home.html", "nav-home")
	other := listen("nav-other")
	aw.View().Eval(`location.href = "other://x/nav.html"`)
	select {
	case <-foreignScheme.opened:
	case <-time.After(askWait):
		t.Fatal("the other scheme's page was never requested, so nothing was tested")
	}
	select {
	case <-other:
		t.Fatal("a page at other://x called into Go after the app's page navigated there")
	case <-time.After(3 * time.Second):
	}
}

func click(t *testing.T, a *app.App, id, target string) *app.AppWindow {
	t.Helper()
	xdotool, err := exec.LookPath("xdotool")
	if err != nil {
		t.Skip("needs xdotool to make a click the webview counts as the user's")
	}
	drain()
	aw := open(t, a, id, "nav-link.html", "nav-link")
	aw.View().Eval(`document.getElementById("out").href = "` + target + `"`)
	time.Sleep(200 * time.Millisecond)
	clicked := listen("nav-clicked")
	x, y := aw.Position()
	w, h := aw.Size()
	if out, err := exec.Command(xdotool, "mousemove", "--sync", strconv.Itoa(x+w/2), strconv.Itoa(y+h/2), "click", "1").CombinedOutput(); err != nil {
		t.Fatalf("xdotool: %v %s", err, out)
	}
	select {
	case <-clicked:
	case <-time.After(askWait):
		t.Fatal("the click never reached the page, so nothing was tested")
	}
	return aw
}

func TestAClickedLinkOutOfTheOriginOpensOutside(t *testing.T) {
	a := harness(t)
	foreign := foreignHTTP(t)
	target := foreign.URL + "/clicked"
	aw := click(t, a, "nav-click", target)
	select {
	case u := <-external:
		if u != target {
			t.Fatalf("opened outside: %s, want %s", u, target)
		}
	case <-time.After(askWait):
		t.Fatal("a clicked link out of the origin was not opened outside")
	}
	if foreign.saw("/rendered/clicked", 2*time.Second) {
		t.Fatal("the clicked page also rendered in the app's window")
	}
	stillHome(t, aw, "nav-click-home")
}

func TestTheAppsOwnFramesStillRender(t *testing.T) {
	harness(t)
	got := askString(t, `new Promise(function(done){
  var f = document.createElement("iframe");
  f.srcdoc = "<p id=x>inline</p>";
  f.onload = function(){
    var g = document.createElement("iframe");
    g.src = "scorix://app/hello.txt";
    g.onload = function(){ done(f.contentDocument.getElementById("x").textContent + "+" + g.contentDocument.body.textContent) };
    document.body.appendChild(g);
  };
  document.body.appendChild(f);
})`)
	if got != "inline+served-by-go" {
		t.Fatalf("frames read %q, want both the srcdoc and the same-origin frame", got)
	}
}

func TestAClickedMailtoLinkOpensOutside(t *testing.T) {
	a := harness(t)
	target := "mailto:someone@example.com"
	aw := click(t, a, "nav-mailto", target)
	select {
	case u := <-external:
		if u != target {
			t.Fatalf("opened outside: %s, want %s", u, target)
		}
	case <-time.After(askWait):
		t.Fatal("a clicked mailto link was not opened outside")
	}
	stillHome(t, aw, "nav-mailto-home")
}

func TestAFrameTheCSPAllowsRendersFromAnotherOrigin(t *testing.T) {
	a := harness(t)
	aw := open(t, a, "nav-frame", "nav-home.html", "nav-home")
	aw.View().Eval(`var f = document.createElement("iframe"); f.src = "` + framed.URL + `/frame"; document.body.appendChild(f)`)
	if !framed.saw("/rendered/frame", askWait) {
		t.Fatal("a frame the CSP's frame-src names did not render")
	}
}
