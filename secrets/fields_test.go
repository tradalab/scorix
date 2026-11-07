package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	s, err := NewWithKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealFieldsSkipsEmptyAndNeverDoubleSeals(t *testing.T) {
	s := testStore(t)
	a, b, c := "pw", "", "key"
	if err := s.SealFields(&a, &b, nil, &c); err != nil {
		t.Fatal(err)
	}
	if !IsEncrypted(a) || b != "" || !IsEncrypted(c) {
		t.Fatalf("a=%q b=%q c=%q", a, b, c)
	}
	sealedOnce := a
	if err := s.SealFields(&a); err != nil || a != sealedOnce {
		t.Fatal("sealing a sealed value changed it")
	}
	if err := s.OpenFields(&a, &b, &c); err != nil || a != "pw" || c != "key" {
		t.Fatalf("round trip: %q %q, %v", a, c, err)
	}
}

func TestOpenFieldsPeelsEnvelopesNestedByOldCode(t *testing.T) {
	s := testStore(t)
	inner, _ := s.EncryptString("pw")
	outer, _ := s.EncryptString(inner)
	if err := s.OpenFields(&outer); err != nil || outer != "pw" {
		t.Fatalf("got %q, %v", outer, err)
	}
}

func TestAFieldThatWillNotOpenIsBlankedAndTheRestStillOpen(t *testing.T) {
	s, other := testStore(t), testStore(t)
	foreign, _ := other.EncryptString("someone else's")
	mine, _ := s.EncryptString("mine")
	plain := "legacy plaintext"
	err := s.OpenFields(&foreign, &mine, &plain)
	if !errors.Is(err, ErrUnreadable) {
		t.Fatalf("err = %v, want ErrUnreadable", err)
	}
	if foreign != "" {
		t.Fatalf("unreadable field kept %q; ciphertext would reach the dialer as a password", foreign)
	}
	if mine != "mine" || plain != "legacy plaintext" {
		t.Fatalf("the readable fields were not opened: %q %q", mine, plain)
	}
}

func TestANilStoreLeavesFieldsAlone(t *testing.T) {
	var s *Store
	v := "plain"
	if s.SealFields(&v) != nil || s.OpenFields(&v) != nil || v != "plain" {
		t.Fatal("nil store changed a field")
	}
}

func TestOpenFromEnvPrefersTheEnvKey(t *testing.T) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	t.Setenv("TEST_SCORIX_KEY", base64.StdEncoding.EncodeToString(key))
	s, err := OpenFromEnv("com.example.never-touched", "TEST_SCORIX_KEY")
	if err != nil {
		t.Fatal(err)
	}
	same, _ := NewWithKey(key)
	tok, _ := s.EncryptString("x")
	if got, err := same.DecryptString(tok); err != nil || got != "x" {
		t.Fatal("the env key was not the one used")
	}
	t.Setenv("TEST_SCORIX_KEY", "not base64!")
	if _, err := OpenFromEnv("com.example.never-touched", "TEST_SCORIX_KEY"); err == nil {
		t.Fatal("a malformed env key was accepted")
	}
}

func TestResealStopsAtTheFirstErrorAndNamesTheRow(t *testing.T) {
	var written []int
	err := Reseal(context.Background(),
		func(context.Context) ([]int, error) { return []int{1, 2, 3}, nil },
		func(_ context.Context, r int) error {
			if r == 2 {
				return errors.New("disk full")
			}
			written = append(written, r)
			return nil
		})
	if err == nil || len(written) != 1 {
		t.Fatalf("err %v, written %v", err, written)
	}
}
