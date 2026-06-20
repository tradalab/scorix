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
	// When set, verify an Ed25519 sig (appcastURL+".sig") over the raw manifest bytes
	// BEFORE trusting any field — blocks manifest tampering/rollback.
	publicKeyB64 string
}

func NewAppcastProvider(url, publicKeyB64 string) *AppcastProvider {
	return &AppcastProvider{appcastURL: url, publicKeyB64: publicKeyB64}
}

func (p *AppcastProvider) CheckForUpdate(ctx context.Context, currentVersion, platformKey string) (*Result, error) {
	if p.appcastURL == "" {
		return nil, fmt.Errorf("appcast_url not configured")
	}

	body, err := httpGet(ctx, defaultClient(), p.appcastURL, nil)
	if err != nil {
		return nil, err
	}

	// FAIL CLOSED: with a key configured, a missing/invalid "<appcast>.sig" refuses the
	// whole manifest. Empty key skips this (back-compat) but FullUpdate still won't install unsigned.
	if p.publicKeyB64 != "" {
		sigBody, err := httpGet(ctx, defaultClient(), p.appcastURL+".sig", nil)
		if err != nil {
			return nil, fmt.Errorf("appcast: fetch manifest signature: %w", err)
		}
		if err := verifyEd25519(p.publicKeyB64, strings.TrimSpace(string(sigBody)), body); err != nil {
			return nil, fmt.Errorf("appcast: manifest signature verification failed (refusing): %w", err)
		}
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
		return &Result{
			HasUpdate:   true,
			NewVersion:  stat.Version,
			Notes:       stat.Notes,
			ArtifactURL: plat.URL,
			SigBase64:   plat.SignatureBase64,
			Elevate:     plat.WithElevatedTask,
		}, nil
	}

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
