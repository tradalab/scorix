package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var registryClient = &http.Client{Timeout: 10 * time.Second}

func registryBase(raw string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	base = strings.TrimRight(strings.TrimSuffix(base, "/api/v1/launcher"), "/")
	if base == "" {
		return "", errors.New("oidc: the id registry URL is required")
	}
	return base, nil
}

// AppRegistry accepts the one client the id registry lists for slug, as both aud and azp.
// base is id/bff, or the /api/v1/launcher URL under it.
func AppRegistry(base, slug string) (Source, error) {
	b, err := registryBase(base)
	if err != nil {
		return nil, err
	}
	if slug == "" {
		return nil, errors.New("oidc: AppRegistry needs the app's slug")
	}
	endpoint := b + "/api/v1/apps/" + url.PathEscape(slug) + "/auth-config"
	return func(ctx context.Context) (Clients, error) {
		var out struct {
			Config struct {
				Issuer   string `json:"issuer"`
				ClientID string `json:"clientId"`
			} `json:"config"`
		}
		if err := getRegistry(ctx, endpoint, &out); err != nil {
			return Clients{}, err
		}
		c := out.Config
		return Clients{Issuer: c.Issuer, Audiences: []string{c.ClientID}, AuthorizedParties: []string{c.ClientID}}, nil
	}, nil
}

// SharedRegistry accepts every client the id registry knows, with no azp check: for a service
// every app calls with its own token.
func SharedRegistry(base string) (Source, error) {
	b, err := registryBase(base)
	if err != nil {
		return nil, err
	}
	endpoint := b + "/api/v1/apps/auth-clients"
	return func(ctx context.Context) (Clients, error) {
		var out struct {
			Issuer    string   `json:"issuer"`
			ClientIDs []string `json:"clientIds"`
		}
		if err := getRegistry(ctx, endpoint, &out); err != nil {
			return Clients{}, err
		}
		return Clients{Issuer: out.Issuer, Audiences: out.ClientIDs}, nil
	}, nil
}

func getRegistry(ctx context.Context, endpoint string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	res, err := registryClient.Do(req)
	if err != nil {
		return fmt.Errorf("id registry %s: %w", endpoint, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("id registry %s: %s: %s", endpoint, res.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return fmt.Errorf("id registry %s: %w", endpoint, err)
	}
	return nil
}
