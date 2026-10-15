package config

import (
	"fmt"
	"os"
	"p2p/access"
	"p2p/cryptoutils"

	"github.com/joho/godotenv"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// DHTProtocolPrefix isolates our DHT from the public IPFS one.
const DHTProtocolPrefix = "/p2pchat"

type Config struct {
	PeerPort    string
	PeerID      string
	PeerPrivKey crypto.PrivKey // Will be used as peer ID
	// PeerAllowlist limits inbound connections to these peers (empty = allow all).
	PeerAllowlist []peer.ID
	//
	RelayPort    string
	RelayID      peer.ID
	RelayPrivKey crypto.PrivKey // WIll be used as peer ID
	RelayIP      string
	// RelayAllowlist limits relay reservations and relayed connections (empty = open relay).
	RelayAllowlist []peer.ID
}

func LoadConfig() (*Config, error) {
	if err := godotenv.Load(".env"); err != nil {
		fmt.Printf("Failed to load .env: %v", err)
	}
	peerPrivKey, err := cryptoutils.DecodeBase64Key(os.Getenv("PEER_ID"))
	if err != nil {
		return nil, fmt.Errorf("failed to decode relay id: %w", err)
	}
	relayPrivKey, err := cryptoutils.DecodeBase64Key(os.Getenv("RELAY_ID"))
	if err != nil {
		return nil, fmt.Errorf("failed to decode relay id: %w", err)
	}
	relayID, err := peer.IDFromPrivateKey(relayPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to extract id from priv key: %w", err)
	}

	peerAllow, err := access.ParseList(os.Getenv("PEER_ALLOWLIST"))
	if err != nil {
		return nil, fmt.Errorf("PEER_ALLOWLIST: %w", err)
	}
	relayAllow, err := access.ParseList(os.Getenv("RELAY_ALLOWLIST"))
	if err != nil {
		return nil, fmt.Errorf("RELAY_ALLOWLIST: %w", err)
	}

	return &Config{
		PeerPort:      os.Getenv("PEER_TCP_PORT"),
		PeerID:        os.Getenv("PEER_ID"),
		PeerPrivKey:   peerPrivKey,
		PeerAllowlist: peerAllow,
		//
		RelayPort:      os.Getenv("RELAY_TCP_PORT"),
		RelayID:        relayID,
		RelayPrivKey:   relayPrivKey,
		RelayIP:        os.Getenv("RELAY_IP"),
		RelayAllowlist: relayAllow,
	}, nil
}
