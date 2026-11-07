package fetch

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func statusServer(t *testing.T, code int) (*httptest.Server, *atomic.Int32) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		http.Error(w, http.StatusText(code), code)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func deadURL() string {
	srv := httptest.NewServer(http.NotFoundHandler())
	u := srv.URL
	srv.Close()
	return u
}

func fetchTo(t *testing.T, open Opener, opt Options) (string, error) {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "model.gguf")
	return dst, Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, open, opt)
}

func TestAnUnreachableOrFailingHostHandsOverToTheNext(t *testing.T) {
	down, downHits := statusServer(t, http.StatusServiceUnavailable)
	good, _ := fileServer(t, content, 0)
	dst, err := fetchTo(t, Mirrors(get(deadURL()), get(down.URL), get(good.URL)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, content) {
		t.Fatal("the file that landed is not the one served")
	}
	if downHits.Load() != 1 {
		t.Fatalf("the failing host was asked %d times in one open, want once before moving on", downHits.Load())
	}
}

func TestAPartFromOneHostIsFinishedByAnotherWithoutStartingOver(t *testing.T) {
	first, firstSeen := fileServer(t, content, len(content)/3)
	second, secondSeen := fileServer(t, content, 0)
	var calls atomic.Int32
	breakAfterFirst := func(ctx context.Context, offset int64) (*http.Response, error) {
		if calls.Add(1) > 1 {
			return nil, errors.New("connection refused")
		}
		return get(first.URL)(ctx, offset)
	}
	if _, err := fetchTo(t, Mirrors(breakAfterFirst, get(second.URL)), Options{}); err != nil {
		t.Fatal(err)
	}
	if len(firstSeen.seen()) != 1 {
		t.Fatalf("first host saw %v", firstSeen.seen())
	}
	got := secondSeen.seen()
	if len(got) != 1 || !strings.HasPrefix(got[0], "bytes=") || got[0] == "bytes=0-" {
		t.Fatalf("second host was asked %v, want a Range from where the first host stopped", got)
	}
}

func TestTheHostThatLastAnsweredIsAskedFirst(t *testing.T) {
	down, downHits := statusServer(t, http.StatusBadGateway)
	cut, _ := fileServer(t, content, len(content)/2)
	if _, err := fetchTo(t, Mirrors(get(down.URL), get(cut.URL)), Options{}); err != nil {
		t.Fatal(err)
	}
	if downHits.Load() != 1 {
		t.Fatalf("a host known to be down was asked %d times, want only the first open", downHits.Load())
	}
}

func TestOnlyEveryHostRefusingForGoodStopsAtOnce(t *testing.T) {
	gone1, _ := statusServer(t, http.StatusNotFound)
	gone2, _ := statusServer(t, http.StatusUnauthorized)
	start := time.Now()
	_, err := fetchTo(t, Mirrors(get(gone1.URL), get(gone2.URL)), Options{})
	if err == nil || time.Since(start) > 400*time.Millisecond {
		t.Fatalf("err %v after %s: two hosts that refuse for good should end it without the retry wait", err, time.Since(start))
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err %q should name what each host said", err)
	}

	gone, _ := statusServer(t, http.StatusNotFound)
	busy, busyHits := statusServer(t, http.StatusServiceUnavailable)
	if _, err := fetchTo(t, Mirrors(get(gone.URL), get(busy.URL)), Options{Retries: 2}); err == nil {
		t.Fatal("no host served the file, yet it succeeded")
	}
	if busyHits.Load() < 2 {
		t.Fatalf("a host that may recover was asked %d times: one 404 elsewhere must not make the whole download permanent", busyHits.Load())
	}
}

func TestOneHostBehavesLikeThePlainOpener(t *testing.T) {
	srv, _ := fileServer(t, content, len(content)/4)
	if _, err := fetchTo(t, Mirrors(get(srv.URL)), Options{}); err != nil {
		t.Fatalf("a single host that drops once should be resumed: %v", err)
	}
}

func TestARangeRefusalAtTheStartIsNotAFileBeingServed(t *testing.T) {
	odd, _ := statusServer(t, http.StatusRequestedRangeNotSatisfiable)
	good, _ := fileServer(t, content, 0)
	if _, err := fetchTo(t, Mirrors(get(odd.URL), get(good.URL)), Options{}); err != nil {
		t.Fatalf("a host answering 416 to a request for the whole file stopped the download: %v", err)
	}
}

func TestAPartAlreadyWholeIsDoneWhenTheHostSaysThereIsNothingLeft(t *testing.T) {
	srv, _ := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(dst+PartSuffix, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, Mirrors(get(srv.URL)), Options{}); err != nil {
		t.Fatalf("a part that is already the whole file, answered 416, should land: %v", err)
	}
}
