package model

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tradalab/scorix/fetch"
)

type Store struct {
	Root string

	swept sync.Once
}

// How long a half-finished download is kept. A part is a resume point and works
// whatever its age, so the only thing this buys is disk back from a model
// nobody will ask for again - and getting it wrong throws away gigabytes with
// no way to tell the user. A month, not a week: the case it is here for is the
// gated 20 GB repo that broke at 12 GB and was never retried, and a week is
// well inside "I will finish this after the holiday".
const partKeptFor = 30 * 24 * time.Hour

// Downloads this process is in the middle of, so the sweep cannot take one.
// mtime does not say: a resume rehashes what is already on disk before writing
// a byte, which for 12 GB is tens of seconds of looking abandoned. And unlink
// succeeds on an open file everywhere but Windows, so the transfer would run to
// completion against a deleted inode and the rename fail with ENOENT, losing
// the lot.
var (
	liveMu sync.Mutex
	live   = map[string]int{}
)

func hold(part string) func() {
	liveMu.Lock()
	live[part]++
	liveMu.Unlock()
	return func() {
		liveMu.Lock()
		if live[part]--; live[part] <= 0 {
			delete(live, part)
		}
		liveMu.Unlock()
	}
}

// Removes half-finished downloads nobody came back for. Called once per Store
// from Get, because that is the only moment the app is already waiting on the
// disk anyway.
func (s *Store) sweepParts() {
	cutoff := time.Now().Add(-partKeptFor)
	_ = filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, fetch.PartSuffix) {
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.ModTime().Before(cutoff) {
			return nil
		}
		liveMu.Lock()
		busy := live[p] > 0
		liveMu.Unlock()
		if !busy {
			_ = os.Remove(p)
		}
		return nil
	})
}

// Root/<source>/<repo>/<revision>/<file>. The names come from a remote listing,
// so each part must stay inside the store: a hub that lists "../../x" must not
// get to write there.
func (s *Store) Path(snap *Snapshot, f File) (string, error) {
	parts := []string{snap.Source, snap.Repo, snap.Revision, f.Path}
	for _, p := range parts {
		if p == "" || !filepath.IsLocal(filepath.FromSlash(p)) {
			return "", fmt.Errorf("model: %q is not a path inside the store", p)
		}
	}
	return filepath.Join(append([]string{s.Root}, mapSlash(parts)...)...), nil
}

func mapSlash(ps []string) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = filepath.FromSlash(p)
	}
	return out
}

type Progress struct {
	File File
	fetch.Progress
}

// Returns the local path of each file, in order. A file already stored at its
// full size is not fetched again: nothing reaches that name unverified.
func (s *Store) Get(ctx context.Context, src Source, snap *Snapshot, files []File, progress func(Progress)) ([]string, error) {
	s.swept.Do(s.sweepParts)
	paths := make([]string, 0, len(files))
	for _, f := range files {
		if l, ok := src.(LocalSource); ok {
			paths = append(paths, l.LocalPath(snap, f))
			continue
		}
		dst, err := s.Path(snap, f)
		if err != nil {
			return nil, err
		}
		if fi, err := os.Stat(dst); err == nil && (f.Size < 0 || fi.Size() == f.Size) {
			paths = append(paths, dst)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		opt := fetch.Options{}
		if progress != nil {
			opt.Progress = func(p fetch.Progress) { progress(Progress{File: f, Progress: p}) }
		}
		open := func(ctx context.Context, offset int64) (*http.Response, error) {
			return src.Open(ctx, snap, f, offset)
		}
		release := hold(dst + fetch.PartSuffix)
		err = fetch.Fetch(ctx, dst, fetch.File{Size: f.Size, Digest: f.Digest}, open, opt)
		release()
		if err != nil {
			return nil, fmt.Errorf("model: %s %s/%s: %w", snap.Source, snap.Repo, f.Path, err)
		}
		paths = append(paths, dst)
	}
	return paths, nil
}

func (s *Store) Has(snap *Snapshot, files []File) bool {
	for _, f := range files {
		dst, err := s.Path(snap, f)
		if err != nil {
			return false
		}
		fi, err := os.Stat(dst)
		if err != nil || (f.Size >= 0 && fi.Size() != f.Size) {
			return false
		}
	}
	return true
}

func splitRef(ref, def string) (name, rev string) {
	name, rev, ok := strings.Cut(ref, "@")
	if !ok || rev == "" {
		rev = def
	}
	return name, rev
}
