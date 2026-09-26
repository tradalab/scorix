package model

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tradalab/scorix/fetch"
)

type HuggingFace struct {
	// https://huggingface.co when empty. Mirrors such as hf-mirror.com speak
	// the same API.
	Endpoint string
	// Nil for public repos. Called per request so a sealed token can change.
	Token  func(ctx context.Context) (string, error)
	Client *http.Client
}

func (h *HuggingFace) Name() string { return "huggingface" }

func (h *HuggingFace) endpoint() string {
	if h.Endpoint == "" {
		return "https://huggingface.co"
	}
	return strings.TrimRight(h.Endpoint, "/")
}

func (h *HuggingFace) client() *http.Client { return noAuthAcrossHosts(h.Client) }

// The token must not follow a redirect to another host. Go only drops it for a
// different domain, and some of a hub's own subdomains are not where a token
// belongs either; the CDN link is already signed.
func noAuthAcrossHosts(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	c := *base
	next := base.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("model: stopped after %d redirects", len(via))
		}
		return nil
	}
	return &c
}

// "owner/repo", or "owner/repo@revision" with a branch, tag or commit.
func (h *HuggingFace) Resolve(ctx context.Context, ref string) (*Snapshot, error) {
	repo, rev := splitRef(ref, "main")
	if strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("model: %q is not owner/repo", repo)
	}
	var info struct {
		SHA string `json:"sha"`
	}
	if err := h.getJSON(ctx, repo, fmt.Sprintf("%s/api/models/%s/revision/%s", h.endpoint(), repo, url.PathEscape(rev)), &info, nil); err != nil {
		return nil, err
	}
	if info.SHA == "" {
		return nil, fmt.Errorf("model: %s@%s: the hub named no commit", repo, rev)
	}
	snap := &Snapshot{Source: h.Name(), Repo: repo, Ref: rev, Revision: info.SHA}
	next := fmt.Sprintf("%s/api/models/%s/tree/%s?recursive=true", h.endpoint(), repo, info.SHA)
	// The next page is a URL the server names, so a hub that points at itself
	// would be followed for ever while the file list grows. A thousand pages is
	// a million files, which no model repo is.
	for page := 0; next != ""; page++ {
		if page >= maxPages {
			return nil, fmt.Errorf("model: %s: the listing did not end after %d pages", repo, maxPages)
		}
		var entries []struct {
			Type string `json:"type"`
			Path string `json:"path"`
			Size int64  `json:"size"`
			OID  string `json:"oid"`
			LFS  *struct {
				OID string `json:"oid"`
			} `json:"lfs"`
		}
		var link string
		if err := h.getJSON(ctx, repo, next, &entries, &link); err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type != "file" {
				continue
			}
			f := File{Path: e.Path, Size: e.Size}
			// Large files carry a sha256 of their content; small ones only the
			// id git gives them, which covers a "blob <size>\0" prefix too.
			if e.LFS != nil && e.LFS.OID != "" {
				f.Digest = &fetch.Digest{Algo: fetch.SHA256, Hex: e.LFS.OID}
			} else if e.OID != "" {
				f.Digest = &fetch.Digest{Algo: fetch.GitBlobSHA1, Hex: e.OID}
			}
			snap.Files = append(snap.Files, f)
		}
		next = nextLink(link)
	}
	return snap, nil
}

func (h *HuggingFace) Open(ctx context.Context, s *Snapshot, f File, offset int64) (*http.Response, error) {
	segs := strings.Split(f.Path, "/")
	for i, p := range segs {
		segs[i] = url.PathEscape(p)
	}
	u := fmt.Sprintf("%s/%s/resolve/%s/%s", h.endpoint(), s.Repo, s.Revision, strings.Join(segs, "/"))
	req, err := h.request(ctx, u)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		defer resp.Body.Close()
		return nil, hubError(s.Repo, resp)
	}
	return resp, nil
}

func (h *HuggingFace) request(ctx context.Context, u string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if h.Token != nil && h.sameHub(req.URL) {
		tok, err := h.Token(ctx)
		if err != nil {
			return nil, fmt.Errorf("model: read token: %w", err)
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	return req, nil
}

// The token belongs to the hub the caller configured and to nothing else. The
// redirect guard covers only hops the client itself takes; a listing's
// `Link: rel="next"` is a URL the server chose, and it arrives here as an
// ordinary request.
func (h *HuggingFace) sameHub(u *url.URL) bool {
	base, err := url.Parse(h.endpoint())
	if err != nil {
		return false
	}
	return u.Host == base.Host && u.Scheme == base.Scheme
}

func (h *HuggingFace) getJSON(ctx context.Context, repo, u string, into any, link *string) error {
	req, err := h.request(ctx, u)
	if err != nil {
		return err
	}
	resp, err := h.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return hubError(repo, resp)
	}
	if link != nil {
		*link = resp.Header.Get("Link")
	}
	// Capped like every other listing this package reads: a hub answering
	// without end would otherwise be read without end.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(into); err != nil {
		return fmt.Errorf("model: %s: unreadable listing: %w", repo, err)
	}
	return nil
}

// A private repo and a missing one both answer 401 to an anonymous caller, and
// only X-Error-Code tells them apart from a gated one.
func hubError(repo string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	msg := strings.TrimSpace(string(body))
	switch code := resp.Header.Get("X-Error-Code"); {
	case code == "GatedRepo":
		// Permanent: what fixes it is a browser and the repo's terms, not
		// another three seconds of trying.
		return fetch.Permanent(fmt.Errorf("%w: %s needs its terms accepted at https://huggingface.co/%s while signed in", ErrGated, repo, repo))
	case code == "RepoNotFound", code == "RevisionNotFound", code == "EntryNotFound", resp.StatusCode == http.StatusNotFound:
		return fetch.Permanent(fmt.Errorf("%w: %s (%s)", ErrNotFound, repo, msg))
	}
	return &fetch.StatusError{Code: resp.StatusCode, Body: msg}
}

const maxPages = 1000

var linkNext = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

func nextLink(h string) string {
	if m := linkNext.FindStringSubmatch(h); m != nil {
		return m[1]
	}
	return ""
}
