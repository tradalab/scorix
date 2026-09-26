// Package fetch downloads one file so that a broken connection costs only what
// was not yet received, and a file that arrives wrong never reaches its final
// name. Model files, runtime binaries and app updates all come through here.
package fetch

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The algorithm travels with the value because sources disagree: Hugging Face
// gives sha256 for large files and a git blob sha1 for small ones, Kaggle md5.
// A CDN's ETag is none of these and is never used.
type Digest struct {
	Algo string
	Hex  string
}

const (
	SHA256 = "sha256"
	// sha1 over "blob <size>\x00" + content, the id git gives a file.
	GitBlobSHA1 = "sha1-gitblob"
	MD5         = "md5"
)

type File struct {
	// -1 when the source does not say.
	Size int64
	// Nil leaves verification to the caller, as the updater does with its
	// signature.
	Digest *Digest
}

// Called again after a broken transfer, so a source whose URLs expire can sign
// a fresh one. offset > 0 asks for the rest of the file.
type Opener func(ctx context.Context, offset int64) (*http.Response, error)

type Progress struct {
	Done, Total int64 // Total is -1 when unknown
}

type Options struct {
	Progress func(Progress)
	// Transfers after the first that a broken connection may use; 3 when zero.
	Retries int
}

var (
	ErrDigest = errors.New("fetch: content does not match its digest")
	ErrSize   = errors.New("fetch: size does not match the source")
)

// Permanent marks an Opener's error as one that trying again cannot help, for
// the reasons only the source knows: terms to accept in a browser, a file that
// is not there. Without it the wait and the "gave up after 4 attempts" read as
// a network having a bad minute, and the thing the user has to go and do is at
// the end of a sentence about something else.
func Permanent(err error) error { return permanent{err} }

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// The status of a response that was not the file.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("fetch: HTTP %d: %s", e.Code, e.Body)
	}
	return fmt.Sprintf("fetch: HTTP %d", e.Code)
}

// A signed URL that expired answers 403; a gated or missing file answers 401 or
// 404, and asking again will not change that.
func (e *StatusError) retryable() bool {
	return e.Code == http.StatusForbidden || e.Code == http.StatusRequestTimeout || e.Code == http.StatusGone ||
		e.Code == http.StatusTooManyRequests || e.Code >= 500
}

// What an unfinished download is called while it is being written. Exported
// because a store that sweeps abandoned parts has to look for the same name,
// and two spellings of it would leak every part instead.
const PartSuffix = ".part"

// Writes through dst+PartSuffix and renames on success. A part left by an earlier,
// interrupted call is resumed. A cancelled ctx keeps the part for next time; a
// wrong size or digest deletes it, since resuming on bad bytes only repeats them.
func Fetch(ctx context.Context, dst string, f File, open Opener, opt Options) (err error) {
	if f.Digest != nil && f.Digest.Algo == GitBlobSHA1 && f.Size < 0 {
		return errors.New("fetch: a git blob digest covers the size, which the source did not give")
	}
	h, err := newHash(f)
	if err != nil {
		return err
	}
	part := dst + PartSuffix
	out, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	var offset int64
	// Registered before the first thing that can fail, or a part that never
	// took a byte escapes it and stays in the store for good.
	defer func() {
		// Nothing was written, so this is not a resume point: it is a file left
		// in the store for every download that never started.
		if err != nil && offset == 0 {
			out.Close()
			_ = os.Remove(part)
		}
	}()
	if offset, err = rehash(out, h, f.Size); err != nil {
		return err
	}

	retries := opt.Retries
	if retries <= 0 {
		retries = 3
	}
	total := f.Size
	report := func() {
		if opt.Progress != nil {
			opt.Progress(Progress{Done: offset, Total: total})
		}
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > retries {
			return fmt.Errorf("fetch: gave up after %d attempts: %w", attempt, lastErr)
		}
		if attempt > 0 {
			if err := sleep(ctx, time.Duration(attempt)*500*time.Millisecond); err != nil {
				return err
			}
		}
		done, err := transfer(ctx, out, h, &offset, &total, f, open, report)
		if done {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var se *StatusError
		if errors.As(err, &se) && !se.retryable() {
			return err
		}
		var perm permanent
		if errors.As(err, &perm) {
			return err
		}
		if errors.Is(err, ErrSize) {
			out.Close()
			os.Remove(part)
			return err
		}
		lastErr = err
	}

	if f.Size >= 0 && offset != f.Size {
		out.Close()
		os.Remove(part)
		return fmt.Errorf("%w: got %d bytes, want %d", ErrSize, offset, f.Size)
	}
	if f.Digest != nil {
		if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, f.Digest.Hex) {
			out.Close()
			os.Remove(part)
			return fmt.Errorf("%w: %s %s, want %s", ErrDigest, f.Digest.Algo, got, f.Digest.Hex)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(part, dst)
}

// One request. done means the whole file is in the part; otherwise err says why
// this transfer stopped, and offset says how far it got.
func transfer(ctx context.Context, out *os.File, h hash.Hash, offset, total *int64, f File, open Opener, report func()) (done bool, err error) {
	resp, err := open(ctx, *offset)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, size, ok := contentRange(resp.Header.Get("Content-Range"))
		if !ok || start != *offset {
			// Bytes from anywhere but where the part ends would be spliced in
			// the wrong place; starting over is the only safe answer.
			if err := restart(out, h, f, offset); err != nil {
				return false, err
			}
			return false, fmt.Errorf("fetch: server resumed at %q, not at %d", resp.Header.Get("Content-Range"), start)
		}
		if size >= 0 {
			*total = size
		}
	case http.StatusOK:
		if *offset > 0 {
			// The server ignored Range and sent the whole file.
			if err := restart(out, h, f, offset); err != nil {
				return false, err
			}
		}
		if resp.ContentLength >= 0 {
			*total = resp.ContentLength
		}
	case http.StatusRequestedRangeNotSatisfiable:
		if f.Size >= 0 && *offset == f.Size {
			return true, nil
		}
		if err := restart(out, h, f, offset); err != nil {
			return false, err
		}
		return false, &StatusError{Code: resp.StatusCode}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if f.Size >= 0 && *total >= 0 && *total != f.Size {
		return false, fmt.Errorf("%w: server has %d bytes, source said %d", ErrSize, *total, f.Size)
	}

	if _, err := out.Seek(*offset, io.SeekStart); err != nil {
		return false, err
	}
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return false, werr
			}
			h.Write(buf[:n])
			*offset += int64(n)
			report()
		}
		if rerr == io.EOF {
			if *total >= 0 && *offset < *total {
				return false, io.ErrUnexpectedEOF
			}
			return true, nil
		}
		if rerr != nil {
			return false, rerr
		}
	}
}

func restart(out *os.File, h hash.Hash, f File, offset *int64) error {
	if err := out.Truncate(0); err != nil {
		return err
	}
	h.Reset()
	writePrefix(h, f)
	*offset = 0
	return nil
}

// Rebuilds the hash over what an earlier call left, so a resumed file is
// verified whole rather than from where this call happened to start.
func rehash(out *os.File, h hash.Hash, size int64) (int64, error) {
	fi, err := out.Stat()
	if err != nil {
		return 0, err
	}
	have := fi.Size()
	if size >= 0 && have > size {
		if err := out.Truncate(0); err != nil {
			return 0, err
		}
		return 0, nil
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	if _, err := io.CopyN(h, out, have); err != nil {
		return 0, err
	}
	return have, nil
}

func newHash(f File) (hash.Hash, error) {
	var h hash.Hash
	switch {
	case f.Digest == nil:
		return nopHash{}, nil
	case f.Digest.Algo == SHA256:
		h = sha256.New()
	case f.Digest.Algo == GitBlobSHA1:
		h = sha1.New()
	case f.Digest.Algo == MD5:
		h = md5.New()
	default:
		return nil, fmt.Errorf("fetch: unknown digest algorithm %q", f.Digest.Algo)
	}
	writePrefix(h, f)
	return h, nil
}

func writePrefix(h hash.Hash, f File) {
	if f.Digest != nil && f.Digest.Algo == GitBlobSHA1 {
		fmt.Fprintf(h, "blob %d\x00", f.Size)
	}
}

// "bytes 100-199/1000" -> 100, 1000. The total is -1 for "*".
func contentRange(v string) (start, total int64, ok bool) {
	rest, found := strings.CutPrefix(v, "bytes ")
	if !found {
		return 0, 0, false
	}
	span, size, found := strings.Cut(rest, "/")
	if !found {
		return 0, 0, false
	}
	first, _, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if size == "*" {
		return start, -1, true
	}
	total, err = strconv.ParseInt(size, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return start, total, true
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type nopHash struct{}

func (nopHash) Write(p []byte) (int, error) { return len(p), nil }
func (nopHash) Sum(b []byte) []byte         { return b }
func (nopHash) Reset()                      {}
func (nopHash) Size() int                   { return 0 }
func (nopHash) BlockSize() int              { return 1 }

// Builds an Opener over plain GET requests, adding Range when resuming. newReq
// runs on every attempt, which is where a source re-signs an expired URL.
func HTTP(client *http.Client, newReq func(ctx context.Context) (*http.Request, error)) Opener {
	if client == nil {
		client = http.DefaultClient
	}
	return func(ctx context.Context, offset int64) (*http.Response, error) {
		req, err := newReq(ctx)
		if err != nil {
			return nil, err
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		return client.Do(req)
	}
}
