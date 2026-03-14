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
		// e.g. "MyApp-windows-amd64.exe" contains "windows-amd64"
		if strings.Contains(strings.ToLower(asset.Name), strings.ToLower(platformKey)) {
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
