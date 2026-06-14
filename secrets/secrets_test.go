package secrets

import (
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestOpenCreatesAndReusesMasterKey(t *testing.T) {
	keyring.MockInit() // in-memory store, no OS access

	s1, err := Open("com.example.test")
	if err != nil {
		t.Fatalf("Open (create): %v", err)
	}
	tok, err := s1.EncryptString("hunter2")
	if err != nil {
		t.Fatal(err)
	}

	// Second Open must load the SAME key — tokens stay readable.
	s2, err := Open("com.example.test")
	if err != nil {
		t.Fatalf("Open (reuse): %v", err)
	}
	got, err := s2.DecryptString(tok)
	if err != nil || got != "hunter2" {
		t.Fatalf("decrypt with reloaded key = %q, %v", got, err)
	}
}

func TestRoundTripAndTokenShape(t *testing.T) {
	keyring.MockInit()
	s, err := Open("com.example.shape")
	if err != nil {
		t.Fatal(err)
	}

	plain := "-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("x", 4096) // > wincred entry limit
	tok, err := s.EncryptString(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(tok) {
		t.Fatalf("token missing prefix: %.20s", tok)
	}
	if tok2, _ := s.EncryptString(plain); tok2 == tok {
		t.Fatal("two encryptions of the same value must differ (random nonce)")
	}
	got, err := s.DecryptString(tok)
	if err != nil || got != plain {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestEmptyAndPassthrough(t *testing.T) {
	keyring.MockInit()
	s, _ := Open("com.example.pass")

	if tok, err := s.EncryptString(""); err != nil || tok != "" {
		t.Fatalf("empty must stay empty: %q, %v", tok, err)
	}
	// Legacy plaintext passes through unchanged (lazy migration contract).
	if got, err := s.DecryptString("plaintext-password"); err != nil || got != "plaintext-password" {
		t.Fatalf("passthrough = %q, %v", got, err)
	}
}

func TestTamperedTokenFails(t *testing.T) {
	keyring.MockInit()
	s, _ := Open("com.example.tamper")
	tok, _ := s.EncryptString("secret")

	flipped := tok[:len(tok)-2] + "AA"
	if _, err := s.DecryptString(flipped); err == nil {
		t.Fatal("tampered token must not decrypt")
	}
	if _, err := s.DecryptString(tokenPrefix + "!!notbase64"); err == nil {
		t.Fatal("malformed base64 must error")
	}
	if _, err := s.DecryptString(tokenPrefix); err == nil {
		t.Fatal("empty token body must error")
	}
}

func TestNewWithKeyValidatesSize(t *testing.T) {
	if _, err := NewWithKey(make([]byte, 16)); err == nil {
		t.Fatal("16-byte key must be rejected (AES-256 only)")
	}
	if _, err := NewWithKey(make([]byte, 32)); err != nil {
		t.Fatalf("32-byte key rejected: %v", err)
	}
}
