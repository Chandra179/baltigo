package cryptoutils

import (
	"bytes"
	"errors"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func newKey(t *testing.T) crypto.PrivKey {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestSealOpen(t *testing.T) {
	bob, eve := newKey(t), newKey(t)
	msg := []byte("hello offline bob")

	sealed, err := Seal(msg, bob.GetPublic())
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != len(msg)+SealOverhead {
		t.Errorf("sealed length %d, want %d", len(sealed), len(msg)+SealOverhead)
	}
	if bytes.Contains(sealed, msg) {
		t.Error("plaintext visible in sealed message")
	}
	got, err := Open(sealed, bob)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("open: %q, %v", got, err)
	}

	if _, err := Open(sealed, eve); !errors.Is(err, ErrOpen) {
		t.Errorf("wrong key: got %v", err)
	}
	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	if _, err := Open(tampered, bob); !errors.Is(err, ErrOpen) {
		t.Errorf("tampered: got %v", err)
	}
	if _, err := Open(sealed[:10], bob); !errors.Is(err, ErrOpen) {
		t.Errorf("truncated: got %v", err)
	}

	// each seal uses a fresh ephemeral key and nonce
	again, _ := Seal(msg, bob.GetPublic())
	if bytes.Equal(sealed, again) {
		t.Error("sealing twice produced identical output")
	}
}

func TestSealRejectsNonEd25519(t *testing.T) {
	_, pub, err := crypto.GenerateECDSAKeyPair(nil)
	if err != nil {
		t.Skip("cannot generate ECDSA key")
	}
	if _, err := Seal([]byte("x"), pub); err == nil {
		t.Error("expected error for non-Ed25519 recipient")
	}
}
