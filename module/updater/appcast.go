package updater

import (
	"context"
	"encoding/json"
	"fmt"
)

type StaticAppcast struct {
	Version   string                      `json:"version"`
	PubDate   string                      `json:"pub_date,omitempty"`
	Notes     string                      `json:"notes,omitempty"`
	Platforms map[string]PlatformArtifact `json:"platforms"`
}

type PlatformArtifact struct {
	URL              string `json:"url"`
	SignatureBase64  string `json:"signature,omitempty"`
	WithElevatedTask bool   `json:"with_elevated_task,omitempty"`
}

type DynamicAppcast struct {
	URL             string `json:"url"`
	Version         string `json:"version"`
	PubDate         string `json:"pub_date,omitempty"`
	Notes           string `json:"notes,omitempty"`
	SignatureBase64 string `json:"signature,omitempty"`
}

type AppcastProvider struct {
	appcastURL string
}

func NewAppcastProvider(url string) *AppcastProvider {
	return &AppcastProvider{appcastURL: url}
}

func (p *AppcastProvider) CheckForUpdate(ctx context.Context, currentVersion, platformKey string) (*Result, error) {
	if p.appcastURL == "" {
		return nil, fmt.Errorf("appcast_url not configured")
	}

	body, err := httpGet(ctx, defaultClient(), p.appcastURL, nil)
	if err != nil {
		return nil, err
	}

	// Try StaticAppcast
	var stat StaticAppcast
	if json.Unmarshal(body, &stat) == nil && stat.Version != "" && len(stat.Platforms) > 0 {
		plat, ok := stat.Platforms[platformKey]
		if !ok {
			return nil, fmt.Errorf("platform %s not found in appcast", platformKey)
		}
		if !isNewer(stat.Version, currentVersion) {
			return &Result{HasUpdate: false}, ErrNoUpdate
		}
		return &Result{
			HasUpdate:   true,
			NewVersion:  stat.Version,
			Notes:       stat.Notes,
			ArtifactURL: plat.URL,
			SigBase64:   plat.SignatureBase64,
			Elevate:     plat.WithElevatedTask,
		}, nil
	}

	// Try DynamicAppcast
	var dyn DynamicAppcast
	if json.Unmarshal(body, &dyn) == nil && dyn.URL != "" && dyn.Version != "" {
		if !isNewer(dyn.Version, currentVersion) {
			return &Result{HasUpdate: false}, ErrNoUpdate
		}
		return &Result{
			HasUpdate:   true,
			NewVersion:  dyn.Version,
			Notes:       dyn.Notes,
			ArtifactURL: dyn.URL,
			SigBase64:   dyn.SignatureBase64,
			Elevate:     false,
		}, nil
	}

	return nil, ErrUnknownAppcastType
}
