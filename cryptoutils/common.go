package cryptoutils

import (
	"encoding/base64"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// DecodeBase64Key decodes a base64-encoded libp2p private key (Ed25519, RSA, ECDSA, ...).
func DecodeBase64Key(base64Key string) (crypto.PrivKey, error) {
	privBytes, err := base64.StdEncoding.DecodeString(base64Key)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(privBytes)
}

// GenerateIdentity generates a new Ed25519 identity. It returns the private key
// as a base64 string (the value to put in PEER_ID or RELAY_ID) and the peer ID
// that other nodes see.
func GenerateIdentity() (key string, id peer.ID, err error) {
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return "", "", fmt.Errorf("failed to generate key: %w", err)
	}
	raw, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal private key: %w", err)
	}
	id, err = peer.IDFromPrivateKey(priv)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(raw), id, nil
}
