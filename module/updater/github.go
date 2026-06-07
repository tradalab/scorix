package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type GitHubProvider struct {
	repo string
}

func NewGitHubProvider(repo string) *GitHubProvider {
	return &GitHubProvider{repo: repo}
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Body    string        `json:"body"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func (p *GitHubProvider) CheckForUpdate(ctx context.Context, currentVersion, platformKey string) (*Result, error) {
	if p.repo == "" {
		return nil, fmt.Errorf("github_repo not configured")
	}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", p.repo)

	// GitHub API usually requires User-Agent.
	// Optional: You can pass a GitHub Token header if hitting rate limits.
	headers := map[string]string{
		"Accept": "application/vnd.github.v3+json",
	}

	body, err := httpGet(ctx, defaultClient(), apiURL, headers)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch github releases: %w", err)
	}

	var release githubRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, fmt.Errorf("failed to parse github release: %w", err)
	}

	if release.TagName == "" {
		return nil, fmt.Errorf("no tag_name found in release")
	}

	if !isNewer(release.TagName, currentVersion) {
		return &Result{HasUpdate: false}, ErrNoUpdate
	}

	// Search for the proper asset matched by platformKey (e.g., windows-amd64)
	var artifactURL string
	var sigURL string

	for _, asset := range release.Assets {
		if assetMatchesPlatform(asset.Name, platformKey) {
			if strings.HasSuffix(asset.Name, ".sig") {
				sigURL = asset.BrowserDownloadURL
			} else if artifactURL == "" {
				// Pick the first non-signature match as the main artifact
				artifactURL = asset.BrowserDownloadURL
			}
		}
	}

	if artifactURL == "" {
		return nil, fmt.Errorf("no asset found matching platform %s in release %s", platformKey, release.TagName)
	}

	res := &Result{
		HasUpdate:   true,
		NewVersion:  release.TagName,
		Notes:       release.Body,
		ArtifactURL: artifactURL,
		Elevate:     false, // GitHub provider relies on global ForceElevate config for this
	}

	// Fetch remote signature if a .sig file was tied to the platformKey
	if sigURL != "" {
		sigBody, err := httpGet(ctx, defaultClient(), sigURL, nil)
		if err == nil && len(sigBody) > 0 {
			res.SigBase64 = strings.TrimSpace(string(sigBody))
		}
	}

	return res, nil
}

// assetMatchesPlatform reports whether a release asset filename corresponds to
// the given platform key ({GOOS}-{GOARCH}, e.g. "darwin-arm64"). It tolerates
// the common naming variants the packager emits: the OS may appear as "macos"
// (darwin) or "win" (windows), the arch may use x86_64/aarch64, and a
// "universal" macOS artifact serves every darwin arch.
func assetMatchesPlatform(name, platformKey string) bool {
	n := strings.ToLower(name)
	pk := strings.ToLower(platformKey)
	if strings.Contains(n, pk) {
		return true
	}

	os, arch, found := strings.Cut(pk, "-")
	if !found {
		return false
	}

	osTokens := map[string][]string{
		"darwin":  {"darwin", "macos", "osx"},
		"windows": {"windows", "win"},
		"linux":   {"linux"},
	}
	tokens, ok := osTokens[os]
	if !ok {
		tokens = []string{os}
	}
	osMatch := false
	for _, t := range tokens {
		if strings.Contains(n, t) {
			osMatch = true
			break
		}
	}
	if !osMatch {
		return false
	}

	archTokens := []string{arch, "universal"}
	switch arch {
	case "amd64":
		archTokens = append(archTokens, "x86_64", "x64")
	case "arm64":
		archTokens = append(archTokens, "aarch64")
	}
	for _, t := range archTokens {
		if strings.Contains(n, t) {
			return true
		}
	}
	return false
}
