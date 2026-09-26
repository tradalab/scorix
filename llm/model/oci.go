package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tradalab/scorix/fetch"
)

// A registry speaking the OCI distribution API. The Ollama library
// (registry.ollama.ai) and Docker Hub's ai/ namespace (docker.io) keep GGUF
// models there as image layers. Pulls are anonymous.
type OCI struct {
	Client *http.Client

	mu     sync.Mutex
	tokens map[string]ociToken
}

type ociToken struct {
	value   string
	expires time.Time
}

func (o *OCI) Name() string { return "oci" }

const (
	mediaOCIManifest    = "application/vnd.oci.image.manifest.v1+json"
	mediaDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	// Docker's model packaging names each file; the older one and Ollama's do not.
	annotationFilePath = "org.cncf.model.filepath"
)

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
}

// "host/name[:tag][@sha256:<hex>]", such as registry.ollama.ai/library/qwen3:0.6b
// or docker.io/ai/smollm2:360M-Q4_K_M. The host is required: Ollama and Docker
// each fill in a different one when it is left out.
func (o *OCI) Resolve(ctx context.Context, ref string) (*Snapshot, error) {
	host, name, tag, pin, err := parseOCIRef(ref)
	if err != nil {
		return nil, err
	}
	api := ociAPIHost(host)
	body, got, err := o.manifest(ctx, api, name, tag, pin)
	if err != nil {
		return nil, fmt.Errorf("model: %s: %w", ref, err)
	}
	var m struct {
		MediaType string          `json:"mediaType"`
		Config    ociDescriptor   `json:"config"`
		Layers    []ociDescriptor `json:"layers"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("model: %s: unreadable manifest: %w", ref, err)
	}
	if len(m.Layers) == 0 {
		return nil, fmt.Errorf("model: %s is a %s, not a single model", ref, m.MediaType)
	}
	snap := &Snapshot{Source: o.Name(), Repo: strings.ReplaceAll(host, ":", "_") + "/" + name, Ref: tag, Revision: got}
	var weights []int
	counts := map[string]int{}
	for _, l := range m.Layers {
		algo, sum, ok := strings.Cut(l.Digest, ":")
		if !ok || algo != fetch.SHA256 {
			return nil, fmt.Errorf("model: %s: layer digest %q is not sha256", ref, l.Digest)
		}
		p := l.Annotations[annotationFilePath]
		if p == "" {
			p = layerName(l.MediaType, sum)
			if p == "" {
				weights = append(weights, len(snap.Files))
			}
		}
		if p != "" {
			if counts[p]++; counts[p] > 1 {
				ext := path.Ext(p)
				p = strings.TrimSuffix(p, ext) + "-" + strconv.Itoa(counts[p]) + ext
			}
		}
		snap.Files = append(snap.Files, File{
			Path: p, Size: l.Size, Digest: &fetch.Digest{Algo: fetch.SHA256, Hex: sum},
			URL: fmt.Sprintf("https://%s/v2/%s/blobs/%s", api, name, l.Digest),
		})
	}
	if len(weights) == 0 {
		return snap, nil
	}
	stem, err := o.modelStem(ctx, api, name, m.Config)
	if err != nil {
		return nil, fmt.Errorf("model: %s: %w", ref, err)
	}
	// Unnamed weights are the shards of one model in layer order: verified
	// against split.no in each file of docker.io/ai/gpt-oss:120B-MXFP4.
	for i, idx := range weights {
		snap.Files[idx].Path = stem + ".gguf"
		if len(weights) > 1 {
			snap.Files[idx].Path = fmt.Sprintf("%s-%05d-of-%05d.gguf", stem, i+1, len(weights))
		}
	}
	return snap, nil
}

// Empty for model weights, which are named once all are counted.
func layerName(mediaType, sum string) string {
	switch mediaType {
	case "application/vnd.ollama.image.model", "application/vnd.docker.ai.gguf.v3":
		return ""
	case "application/vnd.ollama.image.projector", "application/vnd.docker.ai.mmproj":
		return "mmproj.gguf"
	case "application/vnd.ollama.image.license", "application/vnd.docker.ai.license":
		return "LICENSE"
	case "application/vnd.ollama.image.params":
		return "params.json"
	// A Go template for Ollama's own runner; llama-server reads the one in the GGUF.
	case "application/vnd.ollama.image.template":
		return "template"
	case "application/vnd.ollama.image.system":
		return "system.txt"
	}
	return "blobs/sha256-" + sum
}

var ociName = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`)

func parseOCIRef(ref string) (host, name, tag, pin string, err error) {
	rest, pin, _ := strings.Cut(ref, "@")
	if pin != "" {
		pin = strings.ToLower(strings.TrimPrefix(pin, "sha256:"))
		if b, err := hex.DecodeString(pin); err != nil || len(b) != sha256.Size {
			return "", "", "", "", fmt.Errorf("model: %q: the pin must be sha256:<64 hex digits>", ref)
		}
	}
	host, repo, ok := strings.Cut(rest, "/")
	if !ok || !strings.ContainsAny(host, ".:") && host != "localhost" {
		return "", "", "", "", fmt.Errorf("model: %q needs the registry host, such as registry.ollama.ai/library/%s", ref, rest)
	}
	name, tag = repo, ""
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		name, tag = repo[:i], repo[i+1:]
	}
	if tag == "" && pin == "" {
		tag = "latest"
	}
	if !ociName.MatchString(name) {
		return "", "", "", "", fmt.Errorf("model: %q: %q is not a repository name with a namespace", ref, name)
	}
	return host, name, tag, pin, nil
}

func ociAPIHost(host string) string {
	if host == "docker.io" {
		return "registry-1.docker.io"
	}
	return host
}

// The digest is computed from the bytes, not taken from a header: Ollama's
// registry sends none under the standard name and cannot be asked for a
// manifest by digest, so a pin there is checked against what the tag gives.
func (o *OCI) manifest(ctx context.Context, api, name, tag, pin string) ([]byte, string, error) {
	get := func(ref string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://%s/v2/%s/manifests/%s", api, name, ref), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", mediaOCIManifest+", "+mediaDockerManifest)
		return o.do(req)
	}
	var resp *http.Response
	var err error
	if pin != "" {
		resp, err = get("sha256:" + pin)
		if err == nil && resp.StatusCode == http.StatusNotFound && tag != "" {
			resp.Body.Close()
			resp, err = get(tag)
		}
	} else {
		resp, err = get(tag)
	}
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if err := ociStatus(resp); err != nil {
		return nil, "", err
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	s := sha256.Sum256(body)
	got := hex.EncodeToString(s[:])
	if pin != "" && got != pin {
		return nil, "", fmt.Errorf("%w: the tag now points at sha256:%s, not the pinned manifest", ErrNotFound, got)
	}
	return body, got, nil
}

// Neither packaging names the file, so it is named after the repository and,
// when the config says, the quantization, which is what tells two variants
// apart once they sit side by side.
func (o *OCI) modelStem(ctx context.Context, api, name string, config ociDescriptor) (string, error) {
	stem := path.Base(name)
	if config.Digest == "" || config.Size > 1<<20 {
		return stem, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://%s/v2/%s/blobs/%s", api, name, config.Digest), nil)
	if err != nil {
		return "", err
	}
	resp, err := o.do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := ociStatus(resp); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	var c struct {
		FileType string `json:"file_type"`
		Config   struct {
			Quantization string `json:"quantization"`
		} `json:"config"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&c); err != nil {
		return "", fmt.Errorf("unreadable config: %w", err)
	}
	q := c.FileType
	if q == "" {
		q = strings.TrimPrefix(c.Config.Quantization, "MOSTLY_")
	}
	if safeQuant.MatchString(q) {
		stem += "-" + q
	}
	return stem, nil
}

var safeQuant = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

func (o *OCI) Open(ctx context.Context, _ *Snapshot, f File, offset int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := o.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized {
		defer resp.Body.Close()
		return nil, ociStatus(resp)
	}
	return resp, nil
}

// Sends req, and on a 401 that names a token service fetches an anonymous
// token and sends it once more. The blob itself comes from a signed CDN link,
// which the token must not follow.
func (o *OCI) do(req *http.Request) (*http.Response, error) {
	c := noAuthAcrossHosts(o.Client)
	// One token covers a repository's manifests and blobs alike.
	scope, _, _ := strings.Cut(req.URL.Path, "/manifests/")
	scope, _, _ = strings.Cut(scope, "/blobs/")
	scope = req.URL.Host + scope
	if tok, ok := o.cached(scope); ok {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := c.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	params, ok := strings.CutPrefix(challenge, "Bearer ")
	if !ok {
		return resp, nil
	}
	resp.Body.Close()
	tok, err := o.fetchToken(req.Context(), c, parseChallenge(params))
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	if o.tokens == nil {
		o.tokens = map[string]ociToken{}
	}
	o.tokens[scope] = tok
	o.mu.Unlock()
	retry := req.Clone(req.Context())
	retry.Header.Set("Authorization", "Bearer "+tok.value)
	return c.Do(retry)
}

func (o *OCI) cached(scope string) (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.tokens[scope]
	return t.value, ok && time.Now().Before(t.expires)
}

func (o *OCI) fetchToken(ctx context.Context, c *http.Client, ch map[string]string) (ociToken, error) {
	u, err := url.Parse(ch["realm"])
	if err != nil || u.Host == "" {
		return ociToken{}, fmt.Errorf("model: registry names no token service (%q)", ch["realm"])
	}
	q := u.Query()
	for _, k := range []string{"service", "scope"} {
		if v := ch[k]; v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ociToken{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return ociToken{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ociToken{}, fmt.Errorf("model: token service: %w", &fetch.StatusError{Code: resp.StatusCode})
	}
	var out struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return ociToken{}, fmt.Errorf("model: unreadable token: %w", err)
	}
	tok := out.Token
	if tok == "" {
		tok = out.AccessToken
	}
	// The spec's default lifetime; Docker Hub says 300. A little is kept back
	// so a token is not sent in its last second.
	life := 60
	if out.ExpiresIn > 0 {
		life = out.ExpiresIn
	}
	return ociToken{value: tok, expires: time.Now().Add(time.Duration(life)*time.Second - 10*time.Second)}, nil
}

// realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:ai/smollm2:pull"
func parseChallenge(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		k = strings.TrimSpace(strings.TrimLeft(k, ", "))
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.Index(rest[1:], `"`)
			if end < 0 {
				break
			}
			v, rest = rest[1:1+end], rest[2+end:]
		} else {
			v, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.ToLower(k)] = v
		s = rest
	}
	return out
}

// An anonymous caller cannot tell a private repository from a missing one:
// Docker Hub answers both with 401 even after handing out a token.
func ociStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent:
		return nil
	case http.StatusNotFound, http.StatusUnauthorized:
		var e struct {
			Errors []struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"errors"`
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
			msg = e.Errors[0].Code + ": " + e.Errors[0].Message
		}
		// Permanent: a tag that is not there, or a registry that wants
		// credentials, does not become present by asking four times.
		if resp.StatusCode == http.StatusUnauthorized {
			return fetch.Permanent(fmt.Errorf("%w, or private (%s)", ErrNotFound, msg))
		}
		return fetch.Permanent(fmt.Errorf("%w (%s)", ErrNotFound, msg))
	}
	return &fetch.StatusError{Code: resp.StatusCode}
}
