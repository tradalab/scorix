package e2e

import (
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/tradalab/scorix/window"
)

const homePage = `<!doctype html><html><body>home<script>
scorix.invoke("e2e:report", {tag: "nav-home"});
</script></body></html>`

const foreignPage = `<!doctype html><html><body>foreign<script>
scorix.invoke("e2e:report", {tag: "nav-foreign"});
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

var foreignSite = watchedFS{
	FS:     fstest.MapFS{"nav.html": &fstest.MapFile{Data: []byte(foreignPage)}},
	opened: make(chan string, 4),
}

func TestAPageNavigatedToAnotherOriginLosesTheBridge(t *testing.T) {
	a := harness(t)
	home := listen("nav-home")
	foreign := listen("nav-foreign")
	aw, err := a.OpenWindow(window.Options{ID: "nav", Title: "nav", Width: 320, Height: 240, URL: "scorix://app/nav-home.html"})
	if err != nil {
		t.Fatal(err)
	}
	defer aw.Close()
	select {
	case <-home:
	case <-time.After(askWait):
		t.Fatal("the app's own page never reached Go: the control half of this test is broken")
	}

	aw.View().Eval(`location.href = "other://x/nav.html"`)
	select {
	case <-foreignSite.opened:
	case <-time.After(askWait):
		t.Fatal("the foreign page was never requested, so nothing was tested")
	}
	select {
	case <-foreign:
		t.Fatal("a page at other://x called into Go after the app's page navigated there")
	case <-time.After(3 * time.Second):
	}
}
