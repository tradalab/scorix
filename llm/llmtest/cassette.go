package llmtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
)

// A Cassette records the HTTP traffic between a driver and a real server once,
// then plays it back, so the driver's parsing is tested against what the server
// actually sent instead of what its author believed it sends.
//
// Requests match on method, path and body. Headers are neither matched nor
// stored, so a key sent in one stays out of the file - but the query string and
// the request body are both kept, so a driver that authenticates through either
// would record its key into a file that goes on to sit in testdata. None in
// this tree does; check before recording a driver that does.
type Cassette struct {
	path string
	next http.RoundTripper // nil when replaying
	mu   sync.Mutex
	log  []*interaction
}

type interaction struct {
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	Body        json.RawMessage `json:"body,omitempty"`
	Status      int             `json:"status"`
	ContentType string          `json:"content_type,omitempty"`
	Response    string          `json:"response"`
	used        bool
}

func Record(path string, next http.RoundTripper) *Cassette {
	if next == nil {
		next = http.DefaultTransport
	}
	return &Cassette{path: path, next: next}
}

func Replay(path string) (*Cassette, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file struct {
		Interactions []*interaction `json:"interactions"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("llmtest: cassette %s: %w", path, err)
	}
	// Save indents the stored bodies along with everything else.
	for _, it := range file.Interactions {
		it.Body = canonical(it.Body)
	}
	return &Cassette{path: path, log: file.Interactions}, nil
}

func (c *Cassette) Client() *http.Client { return &http.Client{Transport: c} }

func (c *Cassette) Save() error {
	if c.next == nil {
		return errors.New("llmtest: a replayed cassette has nothing new to save")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	raw, err := json.MarshalIndent(map[string]any{"interactions": c.log}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, append(raw, '\n'), 0o644)
}

func (c *Cassette) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		var err error
		if body, err = io.ReadAll(r.Body); err != nil {
			return nil, err
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(body))
	}
	key := interaction{Method: r.Method, Path: r.URL.Path, Body: canonical(body)}
	if r.URL.RawQuery != "" {
		key.Path += "?" + r.URL.RawQuery
	}
	if c.next == nil {
		return c.replay(r, key)
	}
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	rec := key
	rec.Status, rec.ContentType = resp.StatusCode, resp.Header.Get("Content-Type")
	// Kept on Close, not at EOF: a client that stops a stream early closes it
	// half read, and replaying that exchange needs exactly the half it saw.
	resp.Body = &tee{r: resp.Body, done: func(b []byte) {
		rec.Response = string(b)
		c.mu.Lock()
		c.log = append(c.log, &rec)
		c.mu.Unlock()
	}}
	return resp, nil
}

func (c *Cassette) replay(r *http.Request, key interaction) (*http.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, it := range c.log {
		if it.used || it.Method != key.Method || it.Path != key.Path || !bytes.Equal(it.Body, key.Body) {
			continue
		}
		it.used = true
		h := http.Header{}
		if it.ContentType != "" {
			h.Set("Content-Type", it.ContentType)
		}
		return &http.Response{StatusCode: it.Status, Header: h, Body: io.NopCloser(strings.NewReader(it.Response)), Request: r}, nil
	}
	return nil, fmt.Errorf("llmtest: %s has no unused %s %s with this body - was the request changed since recording?", c.path, key.Method, key.Path)
}

// Key order and spacing are the encoder's business, not the request's.
func canonical(body []byte) json.RawMessage {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		if out, err := json.Marshal(v); err == nil {
			return out
		}
	}
	quoted, _ := json.Marshal(string(body))
	return quoted
}

type tee struct {
	r    io.ReadCloser
	buf  bytes.Buffer
	done func([]byte)
	once sync.Once
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.buf.Write(p[:n])
	return n, err
}

func (t *tee) Close() error {
	t.once.Do(func() { t.done(t.buf.Bytes()) })
	return t.r.Close()
}
