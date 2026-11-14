package e2e

import (
	"strings"
	"testing"
)

func TestASchemeResponseCarriesItsContentTypeHeader(t *testing.T) {
	harness(t)
	got := askString(t, `fetch("scorix://app/hello.txt").then(function(r){return r.headers.get("content-type") || ""})`)
	if !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("fetch saw content-type %q: the driver dropped the handler's headers", got)
	}
}

func TestASchemeResponseKeepsTheHandlersStatus(t *testing.T) {
	harness(t)
	if got := askInt(t, `fetch("scorix://app/missing.txt").then(function(r){return r.status})`); got != 404 {
		t.Fatalf("a missing asset answered %d, want 404", got)
	}
}

func TestARangedRequestGetsItsSliceAsPartialContent(t *testing.T) {
	harness(t)
	got := askString(t, `fetch("scorix://app/tone.wav",{headers:{Range:"bytes=8-11"}}).then(function(r){return r.text().then(function(b){return r.status+" "+r.headers.get("content-range")+" "+b})})`)
	if got != "206 bytes 8-11/8044 WAVE" {
		t.Fatalf("a ranged fetch over the scheme answered %q", got)
	}
}
