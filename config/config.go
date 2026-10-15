package config

import (
	"fmt"
	"os"
	"p2p/access"
	"p2p/cryptoutils"

	"github.com/joho/godotenv"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// DHTProtocolPrefix isolates our DHT from the public IPFS one.
const DHTProtocolPrefix = "/p2pchat"

type Config struct {
	PeerPort    string
	PeerID      string
	PeerPrivKey crypto.PrivKey // Will be used as peer ID
	// PeerAllowlist limits inbound connections to these peers (empty = allow all).
	PeerAllowlist []peer.ID
	// PeerDB is the path of the contacts/history database (default peers_db/<peer id>.db).
	PeerDB string
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
		PeerDB:        os.Getenv("PEER_DB"),
		//
		RelayPort:      os.Getenv("RELAY_TCP_PORT"),
		RelayID:        relayID,
		RelayPrivKey:   relayPrivKey,
		RelayIP:        os.Getenv("RELAY_IP"),
		RelayAllowlist: relayAllow,
	}, nil
}

// RelayAddrInfo returns the relay's peer ID with its TCP and QUIC addresses.
func (c *Config) RelayAddrInfo() (peer.AddrInfo, error) {
	addrs, err := c.relayAddrs()
	if err != nil {
		return peer.AddrInfo{}, err
	}
	return peer.AddrInfo{ID: c.RelayID, Addrs: addrs}, nil
}

func (c *Config) relayAddrs() ([]ma.Multiaddr, error) {
	var addrs []ma.Multiaddr
	for _, format := range []string{"/ip4/%s/tcp/%s", "/ip4/%s/udp/%s/quic-v1"} {
		a, err := ma.NewMultiaddr(fmt.Sprintf(format, c.RelayIP, c.RelayPort))
		if err != nil {
			return nil, fmt.Errorf("invalid relay address: %w", err)
		}
		addrs = append(addrs, a)
	}
	return addrs, nil
}

// RelayListenAddrs returns the addresses the relay listens on (TCP and QUIC).
func (c *Config) RelayListenAddrs() []string {
	return []string{
		fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", c.RelayPort),
		fmt.Sprintf("/ip4/0.0.0.0/udp/%s/quic-v1", c.RelayPort),
	}
}
