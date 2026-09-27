package detect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func server(t *testing.T, routes map[string]string) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestEachServerIsKnownByWhatOnlyItAnswers(t *testing.T) {
	ollamaURL := server(t, map[string]string{"/api/version": `{"version":"0.33.3"}`})
	llamaURL := server(t, map[string]string{"/props": `{"build_info":"b9934-32e41fa5b"}`, "/health": `{"status":"ok"}`})
	lmURL := server(t, map[string]string{"/api/v0/models": `{"data":[{"id":"qwen"}]}`, "/v1/models": `{"data":[]}`})
	// Something else on a well-known port: an OpenAI-compatible proxy that is
	// none of the three, and a web app answering everything with HTML.
	proxyURL := server(t, map[string]string{"/v1/models": `{"data":[]}`, "/health": `ok`})
	webURL := server(t, map[string]string{"/api/version": `<html>`, "/props": `<html>`})
	// JSON on the same paths, from some other API: parsing is not identifying.
	otherAPI := server(t, map[string]string{"/api/version": `{"api":"2"}`, "/props": `{"n_ctx":512}`})

	got := scan(context.Background(), []candidate{
		{"ollama", "ollama", ollamaURL, ollamaURL, ollama},
		{"llama-server", "openai", llamaURL, llamaURL + "/v1", llamaServer},
		{"lmstudio", "openai", lmURL, lmURL + "/v1", lmStudio},
		{"llama-server", "openai", proxyURL, "", llamaServer},
		{"lmstudio", "openai", proxyURL, "", lmStudio},
		{"ollama", "ollama", webURL, "", ollama},
		{"llama-server", "openai", webURL, "", llamaServer},
		{"ollama", "ollama", otherAPI, "", ollama},
		{"llama-server", "openai", otherAPI, "", llamaServer},
	})
	if len(got) != 3 {
		t.Fatalf("found %+v", got)
	}
	want := map[string]string{"ollama": "0.33.3", "llama-server": "b9934-32e41fa5b", "lmstudio": ""}
	for _, f := range got {
		if v, ok := want[f.Name]; !ok || v != f.Version || f.BaseURL == "" {
			t.Errorf("%+v", f)
		}
	}
}

// Nothing listening, or something listening that never answers, must cost the
// app a moment, not a stalled start.
func TestNothingRunningIsQuick(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hung.Close()
	start := time.Now()
	got := scan(context.Background(), []candidate{
		{"ollama", "ollama", "http://127.0.0.1:1", "", ollama},
		{"ollama", "ollama", hung.URL, "", ollama},
	})
	if len(got) != 0 || time.Since(start) > 6*time.Second {
		t.Errorf("%v in %s", got, time.Since(start))
	}
}

// Runs against whatever is on the default ports of this machine; logged, not
// asserted, since what runs here is not the test's to decide.
func TestLocalOnThisMachine(t *testing.T) {
	for _, f := range Local(context.Background()) {
		t.Logf("%+v", f)
	}
}
