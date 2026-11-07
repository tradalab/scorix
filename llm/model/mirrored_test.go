package model

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAMirrorResolvesWhenTheHubIsDownAndFilesLandUnderTheHubsName(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	hub.Close()
	src := NewMirrored(&HuggingFace{Endpoint: hub.URL}, &HuggingFace{Endpoint: mirror.URL})
	snap, err := src.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != "huggingface" || snap.Revision != "c0ffee" {
		t.Fatalf("snapshot %+v", snap)
	}
	store := &Store{Root: t.TempDir()}
	paths, err := store.Get(context.Background(), src, snap, snap.Files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range paths {
		if !strings.Contains(filepath.ToSlash(p), "/huggingface/owner/repo/c0ffee/") {
			t.Errorf("stored outside the hub's own place: %s", p)
		}
		if got, _ := os.ReadFile(p); !bytes.Equal(got, mirror.content[snap.Files[i].Path]) {
			t.Errorf("%s differs from what the mirror served", p)
		}
	}
}

func TestAFileTheHubStopsServingComesFromTheMirror(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	src := NewMirrored(&HuggingFace{Endpoint: hub.URL}, &HuggingFace{Endpoint: mirror.URL})
	snap, err := src.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	if _, err := (&Store{Root: t.TempDir()}).Get(context.Background(), src, snap, snap.Files, nil); err != nil {
		t.Fatalf("the mirror did not take over a download the hub dropped: %v", err)
	}
	if mirror.gets["cdn/model-Q4_K_M.gguf"] != 1 {
		t.Fatalf("the mirror served the model %d times, want once", mirror.gets["cdn/model-Q4_K_M.gguf"])
	}
}

func TestTheHubIsPreferredWhileItAnswers(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	src := NewMirrored(&HuggingFace{Endpoint: hub.URL}, &HuggingFace{Endpoint: mirror.URL})
	snap, err := src.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Store{Root: t.TempDir()}).Get(context.Background(), src, snap, snap.Files, nil); err != nil {
		t.Fatal(err)
	}
	if len(mirror.gets) != 0 {
		t.Fatalf("the mirror was used while the hub answered: %v", mirror.gets)
	}
}

func TestARepoNoHostHasIsStillNotFound(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	_, err := NewMirrored(&HuggingFace{Endpoint: hub.URL}, &HuggingFace{Endpoint: mirror.URL}).Resolve(context.Background(), "nobody/here")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestTheHubsOwnNoIsNotOverruledByAMirror(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "other/repo")
	_, err := NewMirrored(&HuggingFace{Endpoint: hub.URL}, &HuggingFace{Endpoint: mirror.URL}).Resolve(context.Background(), "other/repo")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v: the hub answered that the repo does not exist, and a mirror must not supply one", err)
	}
}

type flaky struct {
	Source
	down *bool
}

func (f flaky) Open(ctx context.Context, s *Snapshot, file File, offset int64) (*http.Response, error) {
	if *f.down {
		return nil, errors.New("connection refused")
	}
	return f.Source.Open(ctx, s, file, offset)
}

func TestANewDownloadAsksTheHubFirstAgainOnceItIsBack(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	down := true
	src := NewMirrored(flaky{&HuggingFace{Endpoint: hub.URL}, &down}, &HuggingFace{Endpoint: mirror.URL})
	snap, err := src.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Store{Root: t.TempDir()}).Get(context.Background(), src, snap, snap.Files, nil); err != nil {
		t.Fatal(err)
	}
	down = false
	if _, err := (&Store{Root: t.TempDir()}).Get(context.Background(), src, snap, snap.Files, nil); err != nil {
		t.Fatal(err)
	}
	if n := mirror.gets["cdn/model-Q4_K_M.gguf"]; n != 1 {
		t.Fatalf("the mirror served %d times: once the hub is back a new download should go to it", n)
	}
}

type dropsOnce struct {
	Source
	cut *bool
}

type resetAfter struct {
	io.ReadCloser
	left int
}

func (f *resetAfter) Read(p []byte) (int, error) {
	if f.left <= 0 {
		return 0, errors.New("connection reset")
	}
	if len(p) > f.left {
		p = p[:f.left]
	}
	n, err := f.ReadCloser.Read(p)
	f.left -= n
	return n, err
}

func (c dropsOnce) Open(ctx context.Context, s *Snapshot, file File, offset int64) (*http.Response, error) {
	resp, err := c.Source.Open(ctx, s, file, offset)
	if err == nil && !*c.cut && file.Size > 1000 {
		*c.cut = true
		resp.Body = &resetAfter{ReadCloser: resp.Body, left: int(file.Size / 2)}
	}
	return resp, err
}

type opensCounted struct {
	Source
	opens *int
}

func (c opensCounted) Open(ctx context.Context, s *Snapshot, file File, offset int64) (*http.Response, error) {
	*c.opens++
	return c.Source.Open(ctx, s, file, offset)
}

func TestWithinOneDownloadTheHostThatAnsweredIsAskedFirst(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	down, cut, hubOpens := true, false, 0
	src := NewMirrored(opensCounted{flaky{&HuggingFace{Endpoint: hub.URL}, &down}, &hubOpens}, dropsOnce{&HuggingFace{Endpoint: mirror.URL}, &cut})
	snap, err := src.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	big := []File{snap.Files[0]}
	if _, err := (&Store{Root: t.TempDir()}).Get(context.Background(), src, snap, big, nil); err != nil {
		t.Fatal(err)
	}
	if !cut || hubOpens != 1 {
		t.Fatalf("cut %v, hub opened %d times: after the mirror dropped mid-file the resume should go back to it, not to the dead hub", cut, hubOpens)
	}
}

func TestOpenOutsideTheStoreAlsoFallsOverToTheMirror(t *testing.T) {
	hub := newFakeHub(t, "owner/repo")
	mirror := newFakeHub(t, "owner/repo")
	src := NewMirrored(&HuggingFace{Endpoint: hub.URL}, &HuggingFace{Endpoint: mirror.URL})
	snap, err := src.Resolve(context.Background(), "owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	hub.Close()
	resp, err := src.Open(context.Background(), snap, snap.Files[0], 0)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d from the mirror", resp.StatusCode)
	}
}
