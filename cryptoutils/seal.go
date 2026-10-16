package cryptoutils

import (
	"crypto/rand"
	"crypto/sha512"
	"errors"
	"fmt"

	"filippo.io/edwards25519"
	"github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/nacl/box"
)

// A sealed message is: ephemeral X25519 public key (32) || nonce (24) || box.
// The sender generates a fresh ephemeral key per message and discards it, so
// the sender stays anonymous to whoever stores the message and not even the
// sender can decrypt it later. The recipient is addressed by its libp2p
// Ed25519 identity, converted to X25519.
const (
	keySize   = 32
	nonceSize = 24

	// SealOverhead is the number of bytes Seal adds to a plaintext.
	SealOverhead = keySize + nonceSize + box.Overhead
)

var ErrOpen = errors.New("cannot open sealed message")

// ed25519PubToX25519 converts a libp2p Ed25519 public key to an X25519 public key.
func ed25519PubToX25519(pub crypto.PubKey) (*[keySize]byte, error) {
	if pub.Type() != crypto.Ed25519 {
		return nil, fmt.Errorf("unsupported key type %v, need Ed25519", pub.Type())
	}
	raw, err := pub.Raw()
	if err != nil {
		return nil, err
	}
	p, err := new(edwards25519.Point).SetBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid Ed25519 public key: %w", err)
	}
	var out [keySize]byte
	copy(out[:], p.BytesMontgomery())
	return &out, nil
}

// ed25519PrivToX25519 derives the X25519 private key matching the Ed25519 key.
func ed25519PrivToX25519(priv crypto.PrivKey) (*[keySize]byte, error) {
	if priv.Type() != crypto.Ed25519 {
		return nil, fmt.Errorf("unsupported key type %v, need Ed25519", priv.Type())
	}
	raw, err := priv.Raw() // seed (32) || public key (32)
	if err != nil || len(raw) < keySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	h := sha512.Sum512(raw[:keySize])
	var out [keySize]byte
	copy(out[:], h[:keySize])
	out[0] &= 248
	out[31] &= 127
	out[31] |= 64
	return &out, nil
}

// Seal encrypts plaintext so that only the owner of recipient's private key can read it.
func Seal(plaintext []byte, recipient crypto.PubKey) ([]byte, error) {
	rpub, err := ed25519PubToX25519(recipient)
	if err != nil {
		return nil, err
	}
	epub, epriv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	out := make([]byte, 0, SealOverhead+len(plaintext))
	out = append(out, epub[:]...)
	out = append(out, nonce[:]...)
	return box.Seal(out, plaintext, &nonce, rpub, epriv), nil
}

// Open decrypts a message produced by Seal for the owner of priv.
func Open(sealed []byte, priv crypto.PrivKey) ([]byte, error) {
	if len(sealed) < SealOverhead {
		return nil, ErrOpen
	}
	xpriv, err := ed25519PrivToX25519(priv)
	if err != nil {
		return nil, err
	}
	var epub [keySize]byte
	var nonce [nonceSize]byte
	copy(epub[:], sealed[:keySize])
	copy(nonce[:], sealed[keySize:keySize+nonceSize])
	pt, ok := box.Open(nil, sealed[keySize+nonceSize:], &nonce, &epub, xpriv)
	if !ok {
		return nil, ErrOpen
	}
	return pt, nil
}
