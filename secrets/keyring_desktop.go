//go:build !android

package secrets

import "github.com/zalando/go-keyring"

var errKeyNotFound = keyring.ErrNotFound

func keyGet(service string) (string, error) { return keyring.Get(service, keyAccount) }

func keySet(service, value string) error { return keyring.Set(service, keyAccount, value) }
