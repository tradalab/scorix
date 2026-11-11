// Package oidc reads the bearer token the suite's identity service issues: it verifies the
// signature against the issuer's published keys, then the issuer, audience, authorized party
// and lifetime, and names the caller by the platform uid the token carries.
package oidc

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"

	"github.com/tradalab/scorix/kernel"
	"github.com/tradalab/scorix/observe"
)

const (
	retryInterval   = 30 * time.Second
	refreshInterval = 15 * time.Minute
	foreignInterval = 10 * time.Minute
)

// PersonalOnlyRefusal is the permission_denied message PersonalOnly answers with.
const PersonalOnlyRefusal = "this app supports personal accounts only"

// Clients is what a token is checked against: the issuer, the client ids its aud may name, and
// the client ids its azp may name (none means any).
type Clients struct {
	Issuer            string
	Audiences         []string
	AuthorizedParties []string
}

// Source answers which clients are accepted. It is read on first use (at most every 30 seconds
// while that fails), every 15 minutes in the background, and at most once per 30 seconds when a
// token names an audience it did not list. A failed refresh keeps the last list.
type Source func(ctx context.Context) (Clients, error)

type Config struct {
	// Issuer is the iss every token must carry; its discovery document names the keys.
	Issuer string
	// Audiences are the client ids a token's aud must name at least one of.
	Audiences []string
	// AuthorizedParties, when set, are the only azp a token may carry.
	AuthorizedParties []string
	// JWKSURL skips discovery and reads the keys from this URL.
	JWKSURL string
	// UserClaim names the claim holding the platform uid; empty means "uid".
	UserClaim string
	// Leeway is the clock skew tolerated on exp and nbf; zero means 30s.
	Leeway     time.Duration
	HTTPClient *http.Client
	// Source replaces Audiences and AuthorizedParties, and Issuer when it names one.
	Source Source
	// ForeignParty, when set, lets a token whose azp is not authorized through and reports it.
	ForeignParty func(ctx context.Context, azp, procedure string)
	// PersonalOnly refuses an account the token marks personal_denied, with permission_denied.
	PersonalOnly bool
	// OnAuthenticated runs after the token passes; an error refuses the call as unauthenticated
	// unless it is a *connect.Error, which is returned as is.
	OnAuthenticated func(ctx context.Context, id Identity) error
}

// Identity is what the token says about the caller beyond its uid. Subject is the IdP's own
// id for the person, kept for audit; key data on the Principal's ID instead.
type Identity struct {
	UserID         string
	Subject        string
	Email          string
	Name           string
	PersonalDenied bool
	Claims         jwt.MapClaims
}

type identityKey struct{}

// IdentityFrom reports false when no token authenticated ctx's call, anonymous calls included.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(Identity)
	return id, ok
}

// Authenticator is a kernel.Authenticator for tokens of one issuer. Keys are fetched like a Source
// is read: on first use, every 15 minutes in the background, and at most once per 30 seconds for an
// unknown key id. A call waits for a fetch only while its ctx lives; the fetch itself runs on.
type Authenticator struct {
	cfg                     Config
	now                     func() time.Time
	mu                      sync.Mutex
	keys                    map[string]crypto.PublicKey
	fetched, tried, unknown time.Time
	jwksURL                 string
	lastErr                 error
	clients                 *Clients
	asked, reasked, loaded  time.Time
	sourceErr               error
	fetching                chan struct{}
	fetchTimeout            time.Duration
}

// New checks cfg and returns an Authenticator; it does not reach the issuer yet.
func New(cfg Config) (*Authenticator, error) {
	switch {
	case cfg.Source != nil && (len(cfg.Audiences) > 0 || len(cfg.AuthorizedParties) > 0):
		return nil, errors.New("oidc: Config.Source replaces Audiences and AuthorizedParties; set one or the other")
	case cfg.Source != nil:
	case cfg.Issuer == "":
		return nil, errors.New("oidc: Config.Issuer is required")
	case len(cfg.Audiences) == 0:
		return nil, errors.New("oidc: Config.Audiences is required; a token for any client of the issuer would pass without it")
	}
	if cfg.UserClaim == "" {
		cfg.UserClaim = "uid"
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = 30 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	a := &Authenticator{cfg: cfg, now: time.Now, jwksURL: cfg.JWKSURL, fetching: make(chan struct{}, 1), fetchTimeout: 10 * time.Second}
	if cfg.Source == nil {
		a.clients = &Clients{Issuer: cfg.Issuer, Audiences: cfg.Audiences, AuthorizedParties: cfg.AuthorizedParties}
	}
	return a, nil
}

func unauthenticated(format string, args ...any) error {
	return connect.NewError(connect.CodeUnauthenticated, fmt.Errorf(format, args...))
}

func (a *Authenticator) Authenticate(ctx context.Context, call kernel.Call) (context.Context, error) {
	header := call.Header.Get("Authorization")
	if header == "" {
		if call.Anonymous {
			return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.Anonymous}), nil
		}
		return nil, unauthenticated("no bearer token")
	}
	scheme, raw, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") || raw == "" {
		return nil, unauthenticated("the Authorization header is not a bearer token")
	}
	c, err := a.accepted(ctx)
	if err != nil {
		return nil, err
	}
	claims := jwt.MapClaims{}
	_, err = jwt.ParseWithClaims(raw, claims, a.key(ctx, c.Issuer),
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}),
		jwt.WithIssuer(c.Issuer), jwt.WithExpirationRequired(), jwt.WithLeeway(a.cfg.Leeway), jwt.WithTimeFunc(a.now))
	if ce, ok := errors.AsType[*connect.Error](err); ok {
		return nil, ce
	}
	if err != nil {
		return nil, unauthenticated("token: %v", err)
	}
	aud, _ := claims.GetAudience()
	if !names(aud, c.Audiences) {
		if fresh, ok := a.reask(ctx); ok && names(aud, fresh.Audiences) {
			c = fresh
		} else {
			return nil, unauthenticated("token is for audience %v, not this service", []string(aud))
		}
	}
	if azp, _ := claims["azp"].(string); len(c.AuthorizedParties) > 0 && !slices.Contains(c.AuthorizedParties, azp) {
		if a.cfg.ForeignParty == nil {
			return nil, unauthenticated("token was issued to client %q, which may not call this service", azp)
		}
		a.cfg.ForeignParty(ctx, azp, call.Procedure)
	}
	uid, _ := claims[a.cfg.UserClaim].(string)
	if uid == "" {
		return nil, unauthenticated("token has no %s claim", a.cfg.UserClaim)
	}
	id := Identity{UserID: uid, Claims: claims}
	id.Subject, _ = claims["sub"].(string)
	id.Email, _ = claims["email"].(string)
	id.Name, _ = claims["name"].(string)
	id.PersonalDenied, _ = claims["personal_denied"].(bool)
	if a.cfg.PersonalOnly && id.PersonalDenied {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New(PersonalOnlyRefusal))
	}
	ctx = context.WithValue(ctx, identityKey{}, id)
	if a.cfg.OnAuthenticated != nil {
		if err := a.cfg.OnAuthenticated(ctx, id); err != nil {
			if ce, ok := errors.AsType[*connect.Error](err); ok {
				return nil, ce
			}
			observe.Log(ctx).Error("oidc: OnAuthenticated refused the caller", "user", uid, "error", err.Error())
			return nil, unauthenticated("the caller could not be admitted")
		}
	}
	return kernel.WithPrincipal(ctx, kernel.Principal{Kind: kernel.User, ID: uid}), nil
}

func names(aud jwt.ClaimStrings, want []string) bool {
	return slices.ContainsFunc(aud, func(s string) bool { return slices.Contains(want, s) })
}

func (a *Authenticator) acquire(ctx context.Context, wait bool) bool {
	if !wait {
		select {
		case a.fetching <- struct{}{}:
			return true
		default:
			return false
		}
	}
	select {
	case a.fetching <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (a *Authenticator) release() { <-a.fetching }

func (a *Authenticator) settled(ctx context.Context) {
	if a.acquire(ctx, true) {
		a.release()
	}
}

func (a *Authenticator) detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), a.fetchTimeout)
}

func (a *Authenticator) start(ctx context.Context, wait bool, claim func(now time.Time) bool, run func()) bool {
	if !a.acquire(ctx, wait) {
		return false
	}
	a.mu.Lock()
	ok := claim(a.now())
	a.mu.Unlock()
	if !ok {
		a.release()
		return false
	}
	go func() {
		defer a.release()
		run()
	}()
	return true
}

func (a *Authenticator) firstDue(now time.Time) bool {
	return a.clients == nil && (a.asked.IsZero() || now.Sub(a.asked) >= retryInterval)
}

func (a *Authenticator) accepted(ctx context.Context) (Clients, error) {
	a.mu.Lock()
	now := a.now()
	first := a.firstDue(now)
	stale := a.clients != nil && a.cfg.Source != nil && now.Sub(a.loaded) >= refreshInterval && now.Sub(a.asked) >= retryInterval
	empty := a.clients == nil
	a.mu.Unlock()
	switch {
	case first:
		claim := func(now time.Time) bool {
			if !a.firstDue(now) {
				return false
			}
			a.asked = now
			return true
		}
		if a.start(ctx, true, claim, func() { a.load(ctx) }) {
			a.settled(ctx)
		}
	case stale:
		a.start(ctx, false, func(now time.Time) bool { a.asked = now; return true }, func() {
			if _, err := a.load(ctx); err != nil {
				observe.Log(ctx).Error("oidc: refreshing the accepted clients; keeping the last list", "error", err.Error())
			}
		})
	case empty:
		a.settled(ctx)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.clients != nil {
		return *a.clients, nil
	}
	cause := a.sourceErr
	if cause == nil {
		cause = ctx.Err()
	}
	return Clients{}, connect.NewError(connect.CodeUnavailable, fmt.Errorf("the accepted clients could not be read: %w", cause))
}

func (a *Authenticator) load(ctx context.Context) (Clients, error) {
	fctx, cancel := a.detached(ctx)
	defer cancel()
	c, err := a.ask(fctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.sourceErr = err
		return Clients{}, err
	}
	if a.clients != nil && c.Issuer != a.clients.Issuer {
		observe.Log(ctx).Error("oidc: the source now names another issuer; restart to adopt it", "issuer", c.Issuer, "kept", a.clients.Issuer)
		c.Issuer = a.clients.Issuer
	}
	a.clients, a.loaded, a.sourceErr = &c, a.now(), nil
	return c, nil
}

func (a *Authenticator) reask(ctx context.Context) (Clients, bool) {
	if a.cfg.Source == nil {
		return Clients{}, false
	}
	type loaded struct {
		c   Clients
		err error
	}
	done := make(chan loaded, 1)
	claim := func(now time.Time) bool {
		if !a.reasked.IsZero() && now.Sub(a.reasked) < retryInterval {
			return false
		}
		a.reasked, a.asked = now, now
		return true
	}
	if !a.start(ctx, true, claim, func() {
		c, err := a.load(ctx)
		done <- loaded{c, err}
	}) {
		return Clients{}, false
	}
	select {
	case r := <-done:
		if r.err != nil {
			observe.Log(ctx).Error("oidc: re-reading the accepted clients", "error", r.err.Error())
			return Clients{}, false
		}
		return r.c, true
	case <-ctx.Done():
		return Clients{}, false
	}
}

func (a *Authenticator) ask(ctx context.Context) (Clients, error) {
	c, err := a.cfg.Source(ctx)
	if err != nil {
		return Clients{}, err
	}
	if c.Issuer == "" {
		c.Issuer = a.cfg.Issuer
	}
	c.Audiences = slices.DeleteFunc(slices.Clone(c.Audiences), func(s string) bool { return strings.TrimSpace(s) == "" })
	if c.Issuer == "" || len(c.Audiences) == 0 {
		return Clients{}, fmt.Errorf("the source names issuer %q and %d clients; both are required", c.Issuer, len(c.Audiences))
	}
	return c, nil
}

// LogForeignParties is a Config.ForeignParty that logs each foreign client at most once per
// 10 minutes, so cross-app callers show up before AuthorizedParties is enforced.
func LogForeignParties(log *slog.Logger) func(ctx context.Context, azp, procedure string) {
	return logForeign(log, time.Now)
}

func logForeign(log *slog.Logger, now func() time.Time) func(ctx context.Context, azp, procedure string) {
	var mu sync.Mutex
	seen := map[string]time.Time{}
	return func(ctx context.Context, azp, procedure string) {
		mu.Lock()
		t := now()
		last, ok := seen[azp]
		if ok && t.Sub(last) < foreignInterval {
			mu.Unlock()
			return
		}
		seen[azp] = t
		mu.Unlock()
		log.WarnContext(ctx, "oidc: token from a client outside AuthorizedParties, allowed", "azp", azp, "procedure", procedure)
	}
}

func (a *Authenticator) keyDue(kid string, now time.Time) (due, known bool) {
	_, known = a.keys[kid]
	switch {
	case a.keys == nil:
		due = a.tried.IsZero() || now.Sub(a.tried) >= retryInterval
	case !known:
		due = a.unknown.IsZero() || now.Sub(a.unknown) >= retryInterval
	case now.Sub(a.fetched) > refreshInterval:
		due = now.Sub(a.tried) >= retryInterval
	}
	return due, known
}

func (a *Authenticator) key(ctx context.Context, issuer string) jwt.Keyfunc {
	refresh := func() { a.refresh(ctx, issuer) }
	return func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		a.mu.Lock()
		due, known := a.keyDue(kid, a.now())
		a.mu.Unlock()
		switch {
		case due && known:
			a.start(ctx, false, func(now time.Time) bool { a.tried = now; return true }, refresh)
		case due:
			claim := func(now time.Time) bool {
				if again, have := a.keyDue(kid, now); !again || have {
					return false
				}
				if a.keys != nil {
					a.unknown = now
				}
				a.tried = now
				return true
			}
			if a.start(ctx, true, claim, refresh) {
				a.settled(ctx)
			}
		case !known:
			a.settled(ctx)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		k, known := a.keys[kid]
		switch {
		case known:
			return k, nil
		case a.keys == nil:
			cause := a.lastErr
			if cause == nil {
				cause = ctx.Err()
			}
			return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("the issuer's keys could not be read: %w", cause))
		}
		return nil, fmt.Errorf("no key %q among the issuer's keys", kid)
	}
}

func (a *Authenticator) refresh(ctx context.Context, issuer string) {
	fctx, cancel := a.detached(ctx)
	defer cancel()
	a.mu.Lock()
	url := a.jwksURL
	a.mu.Unlock()
	keys, url, err := a.fetch(fctx, issuer, url)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		a.lastErr = err
		return
	}
	a.keys, a.fetched, a.lastErr, a.jwksURL = keys, a.now(), nil, url
}

func (a *Authenticator) get(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(into)
}

func (a *Authenticator) fetch(ctx context.Context, issuer, jwksURL string) (map[string]crypto.PublicKey, string, error) {
	if jwksURL == "" {
		var doc struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := a.get(ctx, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", &doc); err != nil {
			return nil, "", fmt.Errorf("discovery: %w", err)
		}
		if doc.Issuer != issuer || doc.JWKSURI == "" {
			return nil, "", fmt.Errorf("discovery: the document is for issuer %q with keys at %q, not %q", doc.Issuer, doc.JWKSURI, issuer)
		}
		jwksURL = doc.JWKSURI
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := a.get(ctx, jwksURL, &set); err != nil {
		return nil, "", fmt.Errorf("keys: %w", err)
	}
	keys := map[string]crypto.PublicKey{}
	for _, k := range set.Keys {
		if pub, err := k.public(); err == nil && k.Kid != "" && (k.Use == "" || k.Use == "sig") {
			keys[k.Kid] = pub
		}
	}
	return keys, jwksURL, nil
}

const minRSABits = 2048

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (k jwk) public() (crypto.PublicKey, error) {
	num := func(s string) (*big.Int, error) {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil || len(b) == 0 {
			return nil, fmt.Errorf("key %s: bad number", k.Kid)
		}
		return new(big.Int).SetBytes(b), nil
	}
	switch k.Kty {
	case "RSA":
		n, err := num(k.N)
		if err != nil {
			return nil, err
		}
		if n.BitLen() < minRSABits {
			return nil, fmt.Errorf("key %s: %d-bit RSA is under %d", k.Kid, n.BitLen(), minRSABits)
		}
		e, err := num(k.E)
		if err != nil || !e.IsInt64() || e.Int64() > 1<<31-1 {
			return nil, fmt.Errorf("key %s: bad exponent", k.Kid)
		}
		return &rsa.PublicKey{N: n, E: int(e.Int64())}, nil
	case "EC":
		curve := map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384(), "P-521": elliptic.P521()}[k.Crv]
		if curve == nil {
			return nil, fmt.Errorf("key %s: curve %q", k.Kid, k.Crv)
		}
		x, err := num(k.X)
		if err != nil {
			return nil, err
		}
		y, err := num(k.Y)
		if err != nil {
			return nil, err
		}
		size := (curve.Params().BitSize + 7) / 8
		if x.BitLen() > 8*size || y.BitLen() > 8*size {
			return nil, fmt.Errorf("key %s: point is not on %s", k.Kid, k.Crv)
		}
		point := append([]byte{4}, x.FillBytes(make([]byte, size))...)
		pub, err := ecdsa.ParseUncompressedPublicKey(curve, append(point, y.FillBytes(make([]byte, size))...))
		if err != nil {
			return nil, fmt.Errorf("key %s: %w", k.Kid, err)
		}
		return pub, nil
	}
	return nil, fmt.Errorf("key %s: type %q", k.Kid, k.Kty)
}
