package fetch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var content = bytes.Repeat([]byte("0123456789abcdef"), 64<<10) // 1 MiB

func sum(b []byte) *Digest {
	s := sha256.Sum256(b)
	return &Digest{Algo: SHA256, Hex: hex.EncodeToString(s[:])}
}

type served struct {
	mu     sync.Mutex
	ranges []string
}

func (s *served) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

// ServeContent speaks Range, 206 and 416 the way a real file server does.
func fileServer(t *testing.T, body []byte, cutFirstAfter int) (*httptest.Server, *served) {
	s := &served{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		first := len(s.ranges) == 0
		s.ranges = append(s.ranges, r.Header.Get("Range"))
		s.mu.Unlock()
		if first && cutFirstAfter > 0 {
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body[:cutFirstAfter])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // drops the connection mid-body
		}
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(body))
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func get(url string) Opener {
	return HTTP(nil, func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	})
}

func TestAFileLandsOnlyWhenWholeAndVerified(t *testing.T) {
	srv, _ := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	var last Progress
	err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{Progress: func(p Progress) { last = p }})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, content) {
		t.Error("content differs")
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Error("the part was left behind")
	}
	if last.Done != int64(len(content)) || last.Total != int64(len(content)) {
		t.Errorf("last progress = %+v", last)
	}
}

func TestAWrongDigestNeverReachesTheName(t *testing.T) {
	srv, _ := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum([]byte("other"))}, get(srv.URL), Options{})
	if !errors.Is(err, ErrDigest) {
		t.Fatalf("err = %v", err)
	}
	for _, p := range []string{dst, dst + ".part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after a digest mismatch", filepath.Base(p))
		}
	}
}

func TestABrokenTransferResumesWhereItStopped(t *testing.T) {
	srv, s := fileServer(t, content, 300_000)
	dst := filepath.Join(t.TempDir(), "model.gguf")
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, content) {
		t.Error("content differs")
	}
	r := s.seen()
	if len(r) != 2 || r[0] != "" || !strings.HasPrefix(r[1], "bytes=") || r[1] == "bytes=0-" {
		t.Errorf("requests = %q; the second should ask for the rest only", r)
	}
}

// Hugging Face signs its CDN links for about an hour. A download that outlives
// the link has to go back to the source for a new one, not retry the dead one.
func TestAnExpiredLinkIsSignedAgain(t *testing.T) {
	var signed int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sig") == "1" {
			http.Error(w, "Request has expired", http.StatusForbidden)
			return
		}
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(content))
	}))
	defer srv.Close()
	open := HTTP(nil, func(ctx context.Context) (*http.Request, error) {
		signed++
		return http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s?sig=%d", srv.URL, signed), nil)
	})
	dst := filepath.Join(t.TempDir(), "f")
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, open, Options{}); err != nil {
		t.Fatal(err)
	}
	if signed != 2 {
		t.Errorf("signed %d times", signed)
	}
}

// Splicing a whole file onto the half already on disk would produce something
// that is neither, and only the digest would notice.
func TestAServerThatIgnoresRangeStartsOver(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(dst+".part", content[:1000], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, content) {
		t.Errorf("got %d bytes, not the file", len(got))
	}
}

// A 206 that starts somewhere other than where the part ends would be written
// at the wrong place in the file.
func TestAResumeFromTheWrongPlaceIsNotSpliced(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(content)-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content)
			return
		}
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(dst+".part", content[:1000], 0o600)
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); !bytes.Equal(got, content) || calls != 2 {
		t.Errorf("%d bytes after %d calls", len(got), calls)
	}
}

// A body that ends cleanly short of its length is a broken transfer to resume,
// not a finished one; taking it as finished fails the size check and throws
// away everything received.
func TestACleanEndShortOfTheLengthIsResumed(t *testing.T) {
	var offsets []int64
	open := func(_ context.Context, offset int64) (*http.Response, error) {
		offsets = append(offsets, offset)
		if offset == 0 {
			return &http.Response{StatusCode: 200, ContentLength: int64(len(content)), Body: io.NopCloser(bytes.NewReader(content[:400_000]))}, nil
		}
		h := http.Header{"Content-Range": {fmt.Sprintf("bytes %d-%d/%d", offset, len(content)-1, len(content))}}
		return &http.Response{StatusCode: 206, Header: h, Body: io.NopCloser(bytes.NewReader(content[offset:]))}, nil
	}
	dst := filepath.Join(t.TempDir(), "f")
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, open, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(offsets) != 2 || offsets[1] != 400_000 {
		t.Errorf("offsets = %v", offsets)
	}
}

func TestAPartFromAnEarlierRunIsResumedAndVerifiedWhole(t *testing.T) {
	srv, s := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(dst+".part", content[:500_000], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
	if r := s.seen(); len(r) != 1 || r[0] != "bytes=500000-" {
		t.Errorf("requests = %q", r)
	}

	// A corrupt part is caught by the digest over the whole file, not trusted.
	corrupt := append([]byte("XXXX"), content[4:500_000]...)
	dst2 := filepath.Join(t.TempDir(), "g")
	_ = os.WriteFile(dst2+".part", corrupt, 0o600)
	if err := Fetch(context.Background(), dst2, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); !errors.Is(err, ErrDigest) {
		t.Errorf("a corrupt part passed: %v", err)
	}
}

func TestCancellingKeepsThePartForNextTime(t *testing.T) {
	srv, _ := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "f")
	ctx, cancel := context.WithCancel(context.Background())
	err := Fetch(ctx, dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{Progress: func(p Progress) {
		if p.Done > 200_000 {
			cancel()
		}
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	fi, err := os.Stat(dst + ".part")
	if err != nil || fi.Size() == 0 {
		t.Fatalf("part after cancel: %v", err)
	}
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
}

// `git hash-object` of "hello\n" is the value every git knows.
func TestGitBlobDigestMatchesGit(t *testing.T) {
	body := []byte("hello\n")
	srv, _ := fileServer(t, body, 0)
	dst := filepath.Join(t.TempDir(), "README.md")
	d := &Digest{Algo: GitBlobSHA1, Hex: "ce013625030ba8dba906f756967f9e9ca394464a"}
	if err := Fetch(context.Background(), dst, File{Size: int64(len(body)), Digest: d}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
	if err := Fetch(context.Background(), dst, File{Size: -1, Digest: d}, get(srv.URL), Options{}); err == nil {
		t.Error("a git blob digest without a size was accepted")
	}
}

func TestWhatAskingAgainCannotFixIsNotRetried(t *testing.T) {
	for code, want := range map[int]int{http.StatusNotFound: 1, http.StatusUnauthorized: 1, http.StatusServiceUnavailable: 3} {
		var calls int
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(code)
		}))
		err := Fetch(context.Background(), filepath.Join(t.TempDir(), "f"), File{Size: 10}, get(srv.URL), Options{Retries: 2})
		srv.Close()
		var se *StatusError
		if !errors.As(err, &se) || se.Code != code || calls != want {
			t.Errorf("HTTP %d: %d calls, err %v", code, calls, err)
		}
	}
}

func TestASizeTheServerContradictsStopsAtOnce(t *testing.T) {
	srv, s := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "f")
	err := Fetch(context.Background(), dst, File{Size: 10}, get(srv.URL), Options{})
	if !errors.Is(err, ErrSize) || len(s.seen()) != 1 {
		t.Errorf("err = %v after %d requests", err, len(s.seen()))
	}
	if _, err := os.Stat(dst + ".part"); !os.IsNotExist(err) {
		t.Error("the part of the wrong file was kept")
	}
}

func TestAPartLongerThanTheFileIsDiscarded(t *testing.T) {
	srv, _ := fileServer(t, content, 0)
	dst := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(dst+".part", append(append([]byte{}, content...), "tail"...), 0o600)
	if err := Fetch(context.Background(), dst, File{Size: int64(len(content)), Digest: sum(content)}, get(srv.URL), Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestContentRange(t *testing.T) {
	for in, want := range map[string][3]int64{
		"bytes 100-199/1000": {100, 1000, 1},
		"bytes 0-9/*":        {0, -1, 1},
		"bytes */1000":       {0, 0, 0},
		"items 0-1/2":        {0, 0, 0},
	} {
		start, total, ok := contentRange(in)
		if ok != (want[2] == 1) || (ok && (start != want[0] || total != want[1])) {
			t.Errorf("%q = %d %d %v", in, start, total, ok)
		}
	}
}
