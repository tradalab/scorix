// Package detect finds model servers already running on this machine, so an
// app can offer the user's own Ollama or LM Studio instead of asking for an
// address.
package detect

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type Found struct {
	// "ollama", "llama-server" or "lmstudio".
	Name string
	// The llm driver that speaks to it, and the base URL that driver takes.
	Driver  string
	BaseURL string
	Version string
}

type candidate struct {
	name, driver, root, base string
	// Proves the answer came from this server and not whatever else happens to
	// hold the port: 8080 in particular belongs to half the dev tools there are.
	identify func(ctx context.Context, c *http.Client, root string) (version string, ok bool)
}

var known = []candidate{
	{"ollama", "ollama", "http://127.0.0.1:11434", "http://127.0.0.1:11434", ollama},
	{"llama-server", "openai", "http://127.0.0.1:8080", "http://127.0.0.1:8080/v1", llamaServer},
	{"lmstudio", "openai", "http://127.0.0.1:1234", "http://127.0.0.1:1234/v1", lmStudio},
}

// Probes the default loopback ports of the servers it knows, in parallel.
// Loopback only: a model on the LAN is the user's to name.
func Local(ctx context.Context) []Found { return scan(ctx, known) }

func scan(ctx context.Context, cs []candidate) []Found {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	c := &http.Client{}
	found := make([]*Found, len(cs))
	var wg sync.WaitGroup
	for i, cand := range cs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, ok := cand.identify(ctx, c, cand.root); ok {
				found[i] = &Found{Name: cand.name, Driver: cand.driver, BaseURL: cand.base, Version: v}
			}
		}()
	}
	wg.Wait()
	var out []Found
	for _, f := range found {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}

func getJSON(ctx context.Context, c *http.Client, url string, into any) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(into) == nil
}

func ollama(ctx context.Context, c *http.Client, root string) (string, bool) {
	var v struct {
		Version string `json:"version"`
	}
	ok := getJSON(ctx, c, root+"/api/version", &v)
	return v.Version, ok && v.Version != ""
}

// /props is llama-server's own; build_info is its version.
func llamaServer(ctx context.Context, c *http.Client, root string) (string, bool) {
	var p struct {
		BuildInfo string `json:"build_info"`
	}
	ok := getJSON(ctx, c, root+"/props", &p)
	return p.BuildInfo, ok && p.BuildInfo != ""
}

// LM Studio's native REST API sits beside its OpenAI one; answering both is
// what tells it from any other OpenAI-compatible server on 1234.
func lmStudio(ctx context.Context, c *http.Client, root string) (string, bool) {
	var m struct {
		Data []json.RawMessage `json:"data"`
	}
	ok := getJSON(ctx, c, root+"/api/v0/models", &m)
	return "", ok && m.Data != nil
}
