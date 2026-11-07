package scope

import (
	"strings"
	"testing"

	"github.com/tradalab/scorix/fault"
)

func TestHosts(t *testing.T) {
	h := NewHosts("modules.http.allow", []string{"*.example.com", " API.other.io "})
	for host, want := range map[string]bool{
		"a.example.com":     true,
		"b.c.example.com":   true,
		"example.com":       true,
		"evilexample.com":   false,
		"api.other.io":      true,
		"API.OTHER.IO":      true,
		"api.other.io:8443": true,
		"sub.api.other.io":  false,
	} {
		if got := h.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestEveryKindReadsEmptyAsNothingAndStarAsEverything(t *testing.T) {
	paths, err := NewPaths("p", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if NewHosts("h", nil).Allows("example.com") || NewGlobs("g", []string{" ", ""}).Allows("a/b") || paths.Admits(t.TempDir()) == nil {
		t.Error("an empty list granted something")
	}
	all, err := NewPaths("p", []string{"*"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !NewHosts("h", []string{"*"}).Allows("anything.invalid:1") || !NewGlobs("g", []string{"*"}).Allows("x/y/z") || all.Admits("/nowhere") != nil {
		t.Error(`"*" did not grant everything`)
	}
}

func TestGlobsMatchTheWholeStringAcrossSlashes(t *testing.T) {
	g := NewGlobs("modules.llm.pull_allow", []string{"ollama/hf.co/bartowski/*", "openai/gpt-*-mini"})
	for ref, want := range map[string]bool{
		"ollama/hf.co/bartowski/Qwen-GGUF/q4": true,
		"ollama/hf.co/someone/Sneaky-GGUF":    false,
		"openai/gpt-5-mini":                   true,
		"openai/gpt-5-mini-extra":             false,
		"xopenai/gpt-5-mini":                  false,
	} {
		if got := g.Allows(ref); got != want {
			t.Errorf("Allows(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestADenialNamesTheKeyAndTheValue(t *testing.T) {
	err := NewHosts("modules.http.allow", nil).Deny("evil.example")
	fe, ok := err.(*fault.Error)
	if !ok || fe.Code != fault.CodeDenied {
		t.Fatalf("Deny = %#v, want a fault.CodeDenied", err)
	}
	if !strings.Contains(err.Error(), "modules.http.allow") || fe.Details["scope"] != "modules.http.allow" || fe.Details["value"] != "evil.example" {
		t.Fatalf("Deny = %v %v, want the key and the value in the message and the data", err, fe.Details)
	}
}

func TestStringSaysWhatIsGranted(t *testing.T) {
	for _, c := range []struct {
		h    Hosts
		want string
	}{
		{NewHosts("k", nil), "k: nothing"},
		{NewHosts("k", []string{"*"}), "k: every value"},
		{NewHosts("k", []string{"a.io", "b.io"}), "k: a.io, b.io"},
	} {
		if got := c.h.String(); got != c.want {
			t.Errorf("String = %q, want %q", got, c.want)
		}
	}
}
