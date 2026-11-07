package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// ErrUnreadable means a sealed value did not open on this machine: the keychain entry was recreated, the master
// key rotated, or the value was tampered with. The fix is to type the secret again, not to retry.
var ErrUnreadable = errors.New("secrets: stored value cannot be decrypted on this machine")

// OpenFromEnv opens the store with the base64 32-byte key in env when it is set, else the OS keychain under
// service. The env path is for CI and headless runs that have no keychain.
func OpenFromEnv(service, env string) (*Store, error) {
	if enc := os.Getenv(env); enc != "" {
		key, err := DecodeKey(enc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", env, err)
		}
		return NewWithKey(key)
	}
	return Open(service)
}

// SealFields encrypts each non-empty field in place and leaves already sealed ones as they are, so sealing twice
// never nests. A nil Store leaves every field alone. Pass pointers into a copy when the caller keeps the plaintext.
func (s *Store) SealFields(fields ...*string) error {
	if s == nil {
		return nil
	}
	for i, f := range fields {
		if f == nil || *f == "" || IsEncrypted(*f) {
			continue
		}
		v, err := s.EncryptString(*f)
		if err != nil {
			return fmt.Errorf("secrets: seal field %d: %w", i, err)
		}
		*f = v
	}
	return nil
}

// OpenFields decrypts each field in place, peeling nested envelopes left by older double sealing. A field that
// will not open is set to "" and the call returns ErrUnreadable after opening the rest, so ciphertext never
// reaches a caller as if it were a password. A nil Store leaves every field alone.
func (s *Store) OpenFields(fields ...*string) error {
	if s == nil {
		return nil
	}
	var failed int
	for _, f := range fields {
		if f == nil {
			continue
		}
		v := *f
		for IsEncrypted(v) {
			dec, err := s.DecryptString(v)
			if err != nil {
				v = ""
				failed++
				break
			}
			v = dec
		}
		*f = v
	}
	if failed > 0 {
		return fmt.Errorf("%w (%d field(s))", ErrUnreadable, failed)
	}
	return nil
}

// Reseal reads every row and writes it back, for the one-time move of plaintext rows to sealed ones. update must
// seal on write. Rows are handled in order and the first error stops the run; re-running it is safe.
func Reseal[T any](ctx context.Context, list func(context.Context) ([]T, error), update func(context.Context, T) error) error {
	rows, err := list(ctx)
	if err != nil {
		return fmt.Errorf("secrets: reseal list: %w", err)
	}
	for i, r := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := update(ctx, r); err != nil {
			return fmt.Errorf("secrets: reseal row %d: %w", i, err)
		}
	}
	return nil
}
