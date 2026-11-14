package netx

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func ecdsaHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAnUnpinnedBastionOffersItsEd25519KeyLikeOpenSSHDoes(t *testing.T) {
	b := startBastion(t, ecdsaHostKey(t))
	_, err := sshRoute(t, b, HostKeyPolicy{}).Dial(context.Background(), "tcp", echoServer(t))
	var hk *HostKeyError
	if !errors.As(err, &hk) || hk.Presented.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("err = %v, want the ed25519 key ssh-keyscan and ssh-keygen -lf show first", err)
	}
}

func TestAPinnedEcdsaKeyStillConnectsToABastionThatAlsoHasEd25519(t *testing.T) {
	ec := ecdsaHostKey(t)
	b := startBastion(t, ec)
	pin := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(ec.PublicKey())))
	c, err := sshRoute(t, b, HostKeyPolicy{Pinned: pin}).Dial(context.Background(), "tcp", echoServer(t))
	if err != nil {
		t.Fatalf("a stored ecdsa pin stopped working: %v", err)
	}
	_ = c.Close()
}
