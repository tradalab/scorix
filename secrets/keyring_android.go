//go:build android

package secrets

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tradalab/scorix/internal/hostapi"
)

// Android has no app-writable keychain, so the master key is stored sealed by a Keystore AES key.
var errKeyNotFound = errors.New("secrets: no master key yet")

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
	out, err := hostapi.Keystore.Open(context.Background(), &hostapi.KeystoreData{Alias: service, Data: blob})
	if err != nil {
		return "", err
	}
	return string(out.Data), nil
}

func keySet(service, value string) error {
	out, err := hostapi.Keystore.Seal(context.Background(), &hostapi.KeystoreData{Alias: service, Data: []byte(value)})
	if err != nil {
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
