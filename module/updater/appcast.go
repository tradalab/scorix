package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type StaticAppcast struct {
	Version   string                      `json:"version"`
	PubDate   string                      `json:"pub_date,omitempty"`
	Notes     string                      `json:"notes,omitempty"`
	Platforms map[string]PlatformArtifact `json:"platforms"`
}

type PlatformArtifact struct {
	URLs             []string `json:"urls"` // one per host, tried in order; the artifact need not live where the manifest does
	SignatureBase64  string   `json:"signature,omitempty"`
	WithElevatedTask bool     `json:"with_elevated_task,omitempty"`
}

type DynamicAppcast struct {
	URLs            []string `json:"urls"`
	Version         string   `json:"version"`
	PubDate         string   `json:"pub_date,omitempty"`
	Notes           string   `json:"notes,omitempty"`
	SignatureBase64 string   `json:"signature,omitempty"`
}

type AppcastProvider struct {
	appcastURL string
}

func NewAppcastProvider(url string) *AppcastProvider {
	return &AppcastProvider{appcastURL: url}
}

func (p *AppcastProvider) CheckForUpdate(ctx context.Context, currentVersion, platformKey string) (*Result, error) {
	if p.appcastURL == "" {
		return nil, fmt.Errorf("updater: no appcast endpoint configured")
	}

	body, err := httpGet(ctx, defaultClient(), p.appcastURL, nil)
	if err != nil {
		return nil, err
	}

	var stat StaticAppcast
	if json.Unmarshal(body, &stat) == nil && stat.Version != "" && len(stat.Platforms) > 0 {
		plat, ok := stat.Platforms[platformKey]
		if !ok {
			return nil, fmt.Errorf("platform %s not found in appcast", platformKey)
		}
		if !isNewer(stat.Version, currentVersion) {
			return &Result{HasUpdate: false}, ErrNoUpdate
		}
		if bad, ok := allAdvertise(plat.URLs, stat.Version); !ok {
			return nil, fmt.Errorf("appcast: manifest offers %s but points at %q", stat.Version, bad)
		}
		return &Result{
			HasUpdate:    true,
			NewVersion:   stat.Version,
			Notes:        stat.Notes,
			ArtifactURLs: plat.URLs,
			SigBase64:    plat.SignatureBase64,
			Elevate:      plat.WithElevatedTask,
		}, nil
	}

	var dyn DynamicAppcast
	if json.Unmarshal(body, &dyn) == nil && len(dyn.URLs) > 0 && dyn.Version != "" {
		if !isNewer(dyn.Version, currentVersion) {
			return &Result{HasUpdate: false}, ErrNoUpdate
		}
		if bad, ok := allAdvertise(dyn.URLs, dyn.Version); !ok {
			return nil, fmt.Errorf("appcast: manifest offers %s but points at %q", dyn.Version, bad)
		}
		return &Result{
			HasUpdate:    true,
			NewVersion:   dyn.Version,
			Notes:        dyn.Notes,
			ArtifactURLs: dyn.URLs,
			SigBase64:    dyn.SignatureBase64,
			Elevate:      false,
		}, nil
	}

	return nil, ErrUnknownAppcastType
}

func advertisedIn(artifactURL, version string) bool { // the manifest is unsigned, so the signed artifact's filename must claim the offered version: closes the high-version-to-old-artifact swap
	v := strings.TrimSpace(version)
	if v == "" || artifactURL == "" {
		return false
	}
	name := artifactURL
	if i := strings.LastIndexAny(name, "/\\"); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.IndexByte(name, '?'); i >= 0 {
		name = name[:i]
	}
	return strings.Contains(name, v)
}

func allAdvertise(urls []string, version string) (string, bool) { // every host, not any: the client may pick whichever answers, so one stale entry is enough to land a downgrade
	if len(urls) == 0 {
		return "", false
	}
	for _, u := range urls {
		if !advertisedIn(u, version) {
			return u, false
		}
	}
	return "", true
}
