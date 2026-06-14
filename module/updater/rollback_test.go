package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func genKey(t *testing.T) (pubB64 string, priv ed25519.PrivateKey) {
	t.Helper()
	pub, p, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return base64.StdEncoding.EncodeToString(pub), p
}

func TestVerifyEd25519_Manifest(t *testing.T) {
	pubB64, priv := genKey(t)
	manifest := []byte(`{"version":"1.2.0","platforms":{}}`)
	sigB64 := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, manifest))

	if err := verifyEd25519(pubB64, sigB64, manifest); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	tampered := []byte(`{"version":"9.9.9","platforms":{}}`)
	if err := verifyEd25519(pubB64, sigB64, tampered); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("tampered manifest: got %v, want ErrSignatureInvalid", err)
	}

	otherPubB64, _ := genKey(t)
	if err := verifyEd25519(otherPubB64, sigB64, manifest); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("wrong key: got %v, want ErrSignatureInvalid", err)
	}

	// Missing signature fails closed.
	if err := verifyEd25519(pubB64, "", manifest); !errors.Is(err, ErrSignatureMissing) {
		t.Fatalf("missing signature: got %v, want ErrSignatureMissing", err)
	}
}

func newFloorModule(t *testing.T, currentVersion string) *UpdaterModule {
	t.Helper()
	m := New()
	m.cfg.CurrentVersion = currentVersion
	m.dataDir = t.TempDir()
	return m
}

func TestRollbackFloor_PersistAndRead(t *testing.T) {
	m := newFloorModule(t, "1.0.0")

	if got := m.rollbackFloor(); got != "1.0.0" {
		t.Fatalf("floor without persisted value = %q, want 1.0.0", got)
	}

	m.writeFloor("1.5.0")
	if got := m.rollbackFloor(); got != "1.5.0" {
		t.Fatalf("floor after persist = %q, want 1.5.0", got)
	}

	// A persisted value older than CurrentVersion must not lower the floor.
	m.writeFloor("0.1.0")
	if got := m.rollbackFloor(); got != "1.0.0" {
		t.Fatalf("floor with stale persisted value = %q, want 1.0.0 (CurrentVersion)", got)
	}

	if _, err := os.Stat(filepath.Join(m.dataDir, "updater_floor")); err != nil {
		t.Fatalf("floor file not written under data dir: %v", err)
	}
}

type fakeProvider struct {
	res *Result
	err error
	// gotFloor records the currentVersion the module passed down (the floor).
	gotFloor string
}

func (f *fakeProvider) CheckForUpdate(_ context.Context, currentVersion, _ string) (*Result, error) {
	f.gotFloor = currentVersion
	return f.res, f.err
}

func TestCheckForUpdate_RefusesBelowFloor(t *testing.T) {
	m := newFloorModule(t, "1.0.0")
	m.writeFloor("2.0.0")

	// Provider advertises 1.5.0 — newer than the stale CurrentVersion (1.0.0)
	// but <= the persisted floor (2.0.0). Must be refused.
	fp := &fakeProvider{res: &Result{HasUpdate: true, NewVersion: "1.5.0"}}
	m.provider = fp

	res, err := m.CheckForUpdate(context.Background())
	if !errors.Is(err, ErrNoUpdate) {
		t.Fatalf("rollback to 1.5.0 below floor 2.0.0: got err=%v, want ErrNoUpdate", err)
	}
	if res != nil && res.HasUpdate {
		t.Fatalf("expected HasUpdate=false for below-floor version, got %+v", res)
	}
	if fp.gotFloor != "2.0.0" {
		t.Fatalf("floor passed to provider = %q, want 2.0.0", fp.gotFloor)
	}
}

func TestCheckForUpdate_AcceptsAboveFloor(t *testing.T) {
	m := newFloorModule(t, "1.0.0")
	m.writeFloor("2.0.0")

	fp := &fakeProvider{res: &Result{HasUpdate: true, NewVersion: "2.1.0", ArtifactURL: "https://x/y.msi"}}
	m.provider = fp

	res, err := m.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("update above floor unexpectedly refused: %v", err)
	}
	if res == nil || !res.HasUpdate || res.NewVersion != "2.1.0" {
		t.Fatalf("expected accepted update 2.1.0, got %+v", res)
	}
}
