package model

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"github.com/tradalab/scorix/fetch"
)

// Release assets: where runtime binaries such as llama-server's are published.
type GitHubReleases struct {
	// https://api.github.com when empty.
	Endpoint string
	// Optional; lifts the 60-an-hour anonymous rate limit.
	Token  func(ctx context.Context) (string, error)
	Client *http.Client
}

func (g *GitHubReleases) Name() string { return "github" }

// "owner/repo@tag", or "owner/repo" for the latest release. A tag can be moved
// unless the repo turned on immutable releases, so the asset digests are what
// actually pin the bytes.
func (g *GitHubReleases) Resolve(ctx context.Context, ref string) (*Snapshot, error) {
	repo, tag := splitRef(ref, "latest")
	if strings.Count(repo, "/") != 1 {
		return nil, fmt.Errorf("model: %q is not owner/repo", repo)
	}
	api := g.Endpoint
	if api == "" {
		api = "https://api.github.com"
	}
	u := fmt.Sprintf("%s/repos/%s/releases/tags/%s", strings.TrimRight(api, "/"), repo, url.PathEscape(tag))
	if tag == "latest" {
		u = fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(api, "/"), repo)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != nil {
		if tok, err := g.Token(ctx); err != nil {
			return nil, fmt.Errorf("model: read token: %w", err)
		} else if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	resp, err := g.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s@%s", ErrNotFound, repo, tag)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &fetch.StatusError{Code: resp.StatusCode}
	}
	var rel struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name   string `json:"name"`
			Size   int64  `json:"size"`
			Digest string `json:"digest"`
			URL    string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&rel); err != nil {
		return nil, fmt.Errorf("model: %s: unreadable release: %w", repo, err)
	}
	snap := &Snapshot{Source: g.Name(), Repo: repo, Ref: tag, Revision: rel.TagName}
	for _, a := range rel.Assets {
		f := File{Path: a.Name, Size: a.Size, URL: a.URL}
		// Only assets uploaded since mid-2025 have one; older ones arrive
		// unverified, which is all GitHub offers for them.
		if algo, hexsum, ok := strings.Cut(a.Digest, ":"); ok && algo == fetch.SHA256 {
			f.Digest = &fetch.Digest{Algo: fetch.SHA256, Hex: hexsum}
		}
		snap.Files = append(snap.Files, f)
	}
	return snap, nil
}

func (g *GitHubReleases) Open(ctx context.Context, _ *Snapshot, f File, offset int64) (*http.Response, error) {
	return openURL(ctx, g.client(), f.URL, offset)
}

func (g *GitHubReleases) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}

// A plain link. The sha256 is not optional: without it nothing says the bytes
// that arrived are the ones the user meant.
type URL struct {
	Client *http.Client
}

func (u *URL) Name() string { return "url" }

// "https://host/path/model.gguf#sha256=<hex>".
func (u *URL) Resolve(_ context.Context, ref string) (*Snapshot, error) {
	parsed, err := url.Parse(ref)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return nil, fmt.Errorf("model: %q is not an http(s) link", ref)
	}
	sum, ok := strings.CutPrefix(parsed.Fragment, "sha256=")
	if b, err := hex.DecodeString(sum); !ok || err != nil || len(b) != 32 {
		return nil, fmt.Errorf("model: %q needs #sha256=<64 hex digits>", ref)
	}
	parsed.Fragment = ""
	name := path.Base(parsed.Path)
	if name == "." || name == "/" {
		return nil, fmt.Errorf("model: %q names no file", ref)
	}
	sum = strings.ToLower(sum)
	return &Snapshot{
		// A port's colon is not allowed in a Windows path.
		Source: u.Name(), Repo: strings.ReplaceAll(parsed.Host, ":", "_"), Ref: ref, Revision: sum,
		Files: []File{{Path: name, Size: -1, Digest: &fetch.Digest{Algo: fetch.SHA256, Hex: sum}, URL: parsed.String()}},
	}, nil
}

func (u *URL) Open(ctx context.Context, _ *Snapshot, f File, offset int64) (*http.Response, error) {
	c := u.Client
	if c == nil {
		c = http.DefaultClient
	}
	return openURL(ctx, c, f.URL, offset)
}

func openURL(ctx context.Context, c *http.Client, u string, offset int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	return c.Do(req)
}

// A directory the user already has models in. Nothing is copied or verified:
// the files are theirs and stay where they are.
type Folder struct{}

func (Folder) Name() string { return "folder" }

func (Folder) Resolve(_ context.Context, dir string) (*Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Source: "folder", Repo: abs, Ref: dir}
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(abs, p)
		snap.Files = append(snap.Files, File{Path: filepath.ToSlash(rel), Size: info.Size()})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, dir)
	}
	return snap, err
}

func (Folder) Open(context.Context, *Snapshot, File, int64) (*http.Response, error) {
	return nil, errors.New("model: a folder is read in place, not downloaded")
}

func (Folder) LocalPath(s *Snapshot, f File) string {
	return filepath.Join(s.Repo, filepath.FromSlash(f.Path))
}

var _ LocalSource = Folder{}
