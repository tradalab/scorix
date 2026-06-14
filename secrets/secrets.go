// Package secrets provides at-rest encryption for credentials via envelope
// encryption: a 32-byte master key in the OS credential store (zalando/go-keyring)
// seals values with AES-256-GCM into "scorix:v1:<base64>" tokens.
// DecryptString passes non-token values through unchanged (lazy plaintext migration).
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	tokenPrefix = "scorix:v1:"
	keyAccount  = "scorix-master-key"
	keySize     = 32 // AES-256
)

type Store struct {
	aead cipher.AEAD
}

// Open loads or creates the app's master key in the OS credential store.
// service names the keychain entry (typically the app identifier).
func Open(service string) (*Store, error) {
	key, err := loadOrCreateKey(service)
	if err != nil {
		return nil, err
	}
	return newStore(key)
}

// NewWithKey builds a Store from an externally managed 32-byte key, for tests
// and headless environments without an OS credential store.
func NewWithKey(key []byte) (*Store, error) {
	return newStore(key)
}

func newStore(key []byte) (*Store, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: master key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Store{aead: aead}, nil
}

func loadOrCreateKey(service string) ([]byte, error) {
	if enc, err := keyring.Get(service, keyAccount); err == nil {
		key, decErr := base64.StdEncoding.DecodeString(enc)
		if decErr != nil || len(key) != keySize {
			return nil, fmt.Errorf("secrets: keychain entry for %s is corrupt — refusing to overwrite; delete it manually to re-key (existing tokens become unreadable)", service)
		}
		return key, nil
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return nil, fmt.Errorf("secrets: read OS credential store: %w", err)
	}

	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := keyring.Set(service, keyAccount, base64.StdEncoding.EncodeToString(key)); err != nil {
		return nil, fmt.Errorf("secrets: store master key in OS credential store: %w", err)
	}
	return key, nil
}

func DecodeKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("secrets: key is not valid base64: %w", err)
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", keySize, len(key))
	}
	return key, nil
}

// IsEncrypted reports whether v is a sealed token (vs legacy plaintext).
func IsEncrypted(v string) bool { return strings.HasPrefix(v, tokenPrefix) }

// EncryptString seals plain into a token; empty input stays empty.
func (s *Store) EncryptString(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(plain), nil)
	return tokenPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// DecryptString opens a token; non-token values pass through unchanged.
func (s *Store) DecryptString(v string) (string, error) {
	if !IsEncrypted(v) {
		return v, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, tokenPrefix))
	if err != nil {
		return "", fmt.Errorf("secrets: malformed token: %w", err)
	}
	if len(raw) < s.aead.NonceSize() {
		return "", errors.New("secrets: malformed token: too short")
	}
	nonce, ct := raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("secrets: decrypt failed (wrong key or tampered value): %w", err)
	}
	return string(plain), nil
}
