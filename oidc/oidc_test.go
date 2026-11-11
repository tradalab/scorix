package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/golang-jwt/jwt/v5"

	"github.com/tradalab/scorix/kernel"
)

type idp struct {
	*httptest.Server
	mu      sync.Mutex
	keys    map[string]any
	down    atomic.Bool
	fetches atomic.Int32
	hold    chan struct{}
	delay   time.Duration
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func newIDP(t *testing.T) *idp {
	t.Helper()
	p := &idp{keys: map[string]any{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.down.Load() {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]string{"issuer": p.URL, "jwks_uri": p.URL + "/keys"})
		case "/keys":
			p.fetches.Add(1)
			if p.hold != nil {
				<-p.hold
			}
			time.Sleep(p.delay)
			p.mu.Lock()
			defer p.mu.Unlock()
			var keys []map[string]string
			for kid, k := range p.keys {
				switch k := k.(type) {
				case *rsa.PrivateKey:
					use := "sig"
					if strings.HasPrefix(kid, "enc") {
						use = "enc"
					}
					keys = append(keys, map[string]string{"kty": "RSA", "kid": kid, "use": use, "n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())})
				case *ecdsa.PrivateKey:
					keys = append(keys, map[string]string{"kty": "EC", "kid": kid, "crv": "P-256", "x": b64(k.X.FillBytes(make([]byte, 32))), "y": b64(k.Y.FillBytes(make([]byte, 32)))})
				}
			}
			json.NewEncoder(w).Encode(map[string]any{"keys": keys})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	p.add(t, "rsa1", "rsa")
	return p
}

func (p *idp) add(t *testing.T, kid, kind string) {
	t.Helper()
	var k any
	var err error
	switch kind {
	case "rsa":
		k, err = rsa.GenerateKey(rand.Reader, 2048)
	case "rsa2047":
		k, err = rsa.GenerateKey(rand.Reader, 2047)
	default:
		k, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	}
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.keys[kid] = k
	p.mu.Unlock()
}

func (p *idp) sign(t *testing.T, kid string, claims jwt.MapClaims) string {
	t.Helper()
	p.mu.Lock()
	k := p.keys[kid]
	p.mu.Unlock()
	method := jwt.SigningMethod(jwt.SigningMethodRS256)
	if _, ok := k.(*ecdsa.PrivateKey); ok {
		method = jwt.SigningMethodES256
	}
	tok := jwt.NewWithClaims(method, claims)
	tok.Header["kid"] = kid
	s, err := tok.SignedString(k)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (p *idp) claims(extra jwt.MapClaims) jwt.MapClaims {
	c := jwt.MapClaims{"iss": p.URL, "aud": []string{"links-app"}, "azp": "links-app", "sub": "zitadel-1", "uid": "u-1", "email": "a@b.example", "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range extra {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

func authenticator(t *testing.T, p *idp, cfg Config) *Authenticator {
	t.Helper()
	if cfg.Issuer == "" {
		cfg.Issuer = p.URL
	}
	if cfg.Audiences == nil {
		cfg.Audiences = []string{"links-app"}
	}
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

func enter(a *Authenticator, h http.Header, anonymous bool) (kernel.Principal, Identity, error) {
	ctx, err := a.Authenticate(context.Background(), kernel.Call{Procedure: "/x.Y/Z", Header: h, Anonymous: anonymous})
	if err != nil {
		return kernel.Principal{}, Identity{}, err
	}
	p, _ := kernel.PrincipalFrom(ctx)
	id, _ := IdentityFrom(ctx)
	return p, id, nil
}

func TestAValidTokenNamesTheUserByItsUIDClaim(t *testing.T) {
	p := newIDP(t)
	p.add(t, "ec1", "ec")
	a := authenticator(t, p, Config{})
	for _, kid := range []string{"rsa1", "ec1"} {
		who, id, err := enter(a, bearer(p.sign(t, kid, p.claims(nil))), false)
		if err != nil || who != (kernel.Principal{Kind: kernel.User, ID: "u-1"}) || id.Subject != "zitadel-1" || id.Email != "a@b.example" {
			t.Errorf("%s: %+v %+v %v", kid, who, id, err)
		}
	}
}

func TestEveryTokenThatShouldNotPassIsUnauthenticated(t *testing.T) {
	p := newIDP(t)
	other := newIDP(t)
	a := authenticator(t, p, Config{AuthorizedParties: []string{"links-app"}})
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, p.claims(nil))
	hs.Header["kid"] = "rsa1"
	forged, _ := hs.SignedString([]byte("secret"))
	none := jwt.NewWithClaims(jwt.SigningMethodNone, p.claims(nil))
	none.Header["kid"] = "rsa1"
	unsigned, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	for name, h := range map[string]http.Header{
		"no header":      {},
		"not bearer":     {"Authorization": {"Basic abc"}},
		"other scheme":   {"Authorization": {"Token " + p.sign(t, "rsa1", p.claims(nil))}},
		"garbage":        bearer("not.a.token"),
		"wrong issuer":   bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"iss": "https://elsewhere.example"}))),
		"wrong audience": bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": []string{"other-app"}}))),
		"no audience":    bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"aud": nil}))),
		"expired":        bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()}))),
		"no expiry":      bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"exp": nil}))),
		"not yet":        bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"nbf": time.Now().Add(time.Minute).Unix()}))),
		"no uid":         bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"uid": nil}))),
		"foreign azp":    bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"azp": "other-app"}))),
		"other signer":   bearer(other.sign(t, "rsa1", p.claims(nil))),
		"hmac confusion": bearer(forged),
		"alg none":       bearer(unsigned),
		"no kid":         bearer(noKid(t, p)),
	} {
		if _, _, err := enter(a, h, false); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s = %v, want unauthenticated", name, err)
		}
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"exp": time.Now().Add(-10 * time.Second).Unix()}))), false); err != nil {
		t.Errorf("a token expired within the leeway = %v", err)
	}
}

func noKid(t *testing.T, p *idp) string {
	t.Helper()
	p.mu.Lock()
	k := p.keys["rsa1"]
	p.mu.Unlock()
	s, _ := jwt.NewWithClaims(jwt.SigningMethodRS256, p.claims(nil)).SignedString(k)
	return s
}

func TestAnAnonymousCallWithNoTokenIsAnonymousButABadTokenStillFails(t *testing.T) {
	p := newIDP(t)
	a := authenticator(t, p, Config{})
	if who, _, err := enter(a, http.Header{}, true); err != nil || who.Kind != kernel.Anonymous {
		t.Fatalf("anonymous = %+v %v", who, err)
	}
	if _, _, err := enter(a, bearer("not.a.token"), true); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a bad token on an anonymous route = %v", err)
	}
	if who, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), true); err != nil || who.Kind != kernel.User {
		t.Fatalf("a good token on an anonymous route = %+v %v", who, err)
	}
}

func TestARotatedKeyIsFetchedOnceAndAnUnknownKidCannotHammerTheIDP(t *testing.T) {
	p := newIDP(t)
	a := authenticator(t, p, Config{})
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false)
	before := p.fetches.Load()
	p.add(t, "rsa2", "rsa")
	if _, _, err := enter(a, bearer(p.sign(t, "rsa2", p.claims(nil))), false); err != nil {
		t.Fatalf("a token under a rotated key = %v", err)
	}
	if p.fetches.Load() != before+1 {
		t.Fatalf("%d fetches for one rotation", p.fetches.Load()-before)
	}
	for range 20 {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, p.claims(nil))
		tok.Header["kid"] = "nobody"
		p.mu.Lock()
		s, _ := tok.SignedString(p.keys["rsa1"])
		p.mu.Unlock()
		enter(a, bearer(s), false)
	}
	if n := p.fetches.Load() - before; n != 1 {
		t.Fatalf("20 unknown kids cost %d more fetches, want none within the interval", n-1)
	}
	now = now.Add(time.Minute)
	p.add(t, "rsa3", "rsa")
	if _, _, err := enter(a, bearer(p.sign(t, "rsa3", p.claims(nil))), false); err != nil {
		t.Fatalf("a rotation after the interval = %v", err)
	}
}

func TestAnIDPThatIsDownIsUnavailableUntilItComesBack(t *testing.T) {
	p := newIDP(t)
	p.down.Store(true)
	a := authenticator(t, p, Config{})
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	tok := p.sign(t, "rsa1", p.claims(nil))
	if _, _, err := enter(a, bearer(tok), false); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("with the IdP down = %v, want unavailable", err)
	}
	p.down.Store(false)
	if _, _, err := enter(a, bearer(tok), false); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("before the retry interval = %v, want unavailable without a fetch", err)
	}
	now = now.Add(time.Minute)
	if _, _, err := enter(a, bearer(tok), false); err != nil {
		t.Fatalf("after the IdP came back = %v", err)
	}
}

func TestAConfigThatWouldCheckNothingIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		cfg  Config
		want string
	}{
		"no issuer":   {Config{Audiences: []string{"a"}}, "Issuer is required"},
		"no audience": {Config{Issuer: "https://id.example"}, "Audiences is required"},
	} {
		if _, err := New(c.cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestADiscoveryDocumentForAnotherIssuerIsRefused(t *testing.T) {
	p := newIDP(t)
	a := authenticator(t, p, Config{Issuer: p.URL + "/"})
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(jwt.MapClaims{"iss": p.URL + "/"}))), false); connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "discovery") {
		t.Fatalf("mismatched discovery = %v", err)
	}
}

func TestARevokedKeyStopsWorkingAtTheNextRefresh(t *testing.T) {
	p := newIDP(t)
	a := authenticator(t, p, Config{})
	now := time.Unix(10_000, 0)
	a.now = func() time.Time { return now }
	tok := p.sign(t, "rsa1", p.claims(jwt.MapClaims{"exp": now.Add(time.Hour).Unix()}))
	if _, _, err := enter(a, bearer(tok), false); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	delete(p.keys, "rsa1")
	p.mu.Unlock()
	now = now.Add(10 * time.Minute)
	if _, _, err := enter(a, bearer(tok), false); err != nil {
		t.Fatalf("a cached key before the refresh = %v", err)
	}
	now = now.Add(6 * time.Minute)
	waitFor(t, func() bool {
		_, _, err := enter(a, bearer(tok), false)
		return connect.CodeOf(err) == connect.CodeUnauthenticated
	})
}

func TestAKeyPublishedForEncryptionNeverVerifiesASignature(t *testing.T) {
	p := newIDP(t)
	p.add(t, "enc1", "rsa")
	a := authenticator(t, p, Config{})
	if _, _, err := enter(a, bearer(p.sign(t, "enc1", p.claims(nil))), false); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a token signed by an encryption key = %v", err)
	}
}

func TestAnRSAKeyOfUnder2048BitsNeverVerifiesASignature(t *testing.T) {
	p := newIDP(t)
	p.add(t, "weak", "rsa2047")
	a := authenticator(t, p, Config{})
	if _, _, err := enter(a, bearer(p.sign(t, "weak", p.claims(nil))), false); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("a token signed by a 2047-bit key = %v", err)
	}
	if _, _, err := enter(a, bearer(p.sign(t, "rsa1", p.claims(nil))), false); err != nil {
		t.Fatalf("the 2048-bit key beside it = %v", err)
	}
}
