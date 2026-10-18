//go:build android

package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tradalab/scorix/native"
)

// Android has no app-writable keychain, so the master key is stored sealed by a Keystore AES key.
var errKeyNotFound = errors.New("secrets: no master key yet")

type sealed struct {
	Data []byte `json:"data"`
}

func keyPath(service string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "scorix-keystore", service+".key"), nil
}

func keyGet(service string) (string, error) {
	p, err := keyPath(service)
	if err != nil {
		return "", err
	}
	blob, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errKeyNotFound
	}
	if err != nil {
		return "", err
	}
	var out sealed
	if err := native.Call(context.Background(), "keystore", "open", map[string]any{"alias": service, "data": blob}, &out); err != nil {
		return "", err
	}
	return string(out.Data), nil
}

func keySet(service, value string) error {
	var out sealed
	if err := native.Call(context.Background(), "keystore", "seal", map[string]any{"alias": service, "data": []byte(value)}, &out); err != nil {
		return err
	}
	p, err := keyPath(service)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, out.Data, 0o600)
}
