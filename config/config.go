package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/Chandra179/sabaody/access"
	"github.com/Chandra179/sabaody/cryptoutils"

	"github.com/joho/godotenv"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// DHTProtocolPrefix isolates our DHT from the public IPFS one.
const DHTProtocolPrefix = "/p2pchat"

// Modes the program can run in.
const (
	ModePeer  = "peer"
	ModeRelay = "relay"
)

type Config struct {
	// Warnings are non-fatal problems found while loading (e.g. deprecated settings).
	Warnings []string

	// PeerPrivKey is the identity of this peer (peer mode only).
	PeerPrivKey crypto.PrivKey
	// PeerAllowlist limits inbound connections to these peers (empty = allow all).
	PeerAllowlist []peer.ID
	// DisableReceipts (SEND_RECEIPTS=false) stops us from sending delivery receipts.
	DisableReceipts bool
	// DownloadDir (PEER_DOWNLOAD_DIR) is where received files go (default "downloads").
	DownloadDir string
	// MaxFileSize (PEER_MAX_FILE_MB) is the largest file accepted, in bytes (0 = default 100 MiB).
	MaxFileSize int64
	// PeerDB is the path of the contacts/history database (default peers_db/<peer id>.db).
	PeerDB string

	// RelayIP and RelayPort say where the relay is reachable (TCP and UDP/QUIC share the port).
	RelayIP   string
	RelayPort string
	// RelayID is the relay's peer ID. Peers get it from RELAY_PEER_ID; the relay derives it from its key.
	RelayID peer.ID
	// RelayPrivKey is the relay's identity (relay mode only).
	RelayPrivKey crypto.PrivKey
	// RelayAllowlist limits relay reservations and relayed connections (empty = open relay).
	RelayAllowlist []peer.ID
	// RelayDB is the path of the relay mailbox database (default relay_db/mailbox.db).
	RelayDB string
}

// LoadConfig reads the configuration for the given mode from the environment
// and an optional .env file. Only the settings a mode needs are required: a
// peer needs its own key and the relay's public peer ID, the relay needs only
// its own key.
func LoadConfig(mode string) (*Config, error) {
	if mode != ModePeer && mode != ModeRelay {
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("failed to read .env: %w", err)
	}

	c := &Config{
		RelayIP:         os.Getenv("RELAY_IP"),
		RelayPort:       os.Getenv("RELAY_TCP_PORT"),
		DownloadDir:     os.Getenv("PEER_DOWNLOAD_DIR"),
		PeerDB:          os.Getenv("PEER_DB"),
		RelayDB:         os.Getenv("RELAY_DB"),
		DisableReceipts: strings.EqualFold(os.Getenv("SEND_RECEIPTS"), "false"),
	}
	if c.RelayIP == "" {
		return nil, errors.New("RELAY_IP is required")
	}
	if port, err := strconv.Atoi(c.RelayPort); err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("RELAY_TCP_PORT must be a port number, got %q", c.RelayPort)
	}
	if _, err := c.relayAddrs(); err != nil {
		return nil, err
	}

	var err error
	if c.PeerAllowlist, err = access.ParseList(os.Getenv("PEER_ALLOWLIST")); err != nil {
		return nil, fmt.Errorf("PEER_ALLOWLIST: %w", err)
	}
	if c.RelayAllowlist, err = access.ParseList(os.Getenv("RELAY_ALLOWLIST")); err != nil {
		return nil, fmt.Errorf("RELAY_ALLOWLIST: %w", err)
	}
	if v := os.Getenv("PEER_MAX_FILE_MB"); v != "" {
		mb, err := strconv.ParseInt(v, 10, 64)
		if err != nil || mb < 1 {
			return nil, fmt.Errorf("PEER_MAX_FILE_MB must be a positive number, got %q", v)
		}
		c.MaxFileSize = mb << 20
	}

	switch mode {
	case ModeRelay:
		err = c.loadRelayIdentity()
	case ModePeer:
		err = c.loadPeerIdentity()
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) loadRelayIdentity() error {
	key := os.Getenv("RELAY_ID")
	if key == "" {
		return errors.New("RELAY_ID (the relay's private key) is required; create one with --genkey")
	}
	priv, err := cryptoutils.DecodeBase64Key(key)
	if err != nil {
		return fmt.Errorf("RELAY_ID is not a valid private key: %w", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("RELAY_ID: %w", err)
	}
	c.RelayPrivKey, c.RelayID = priv, id
	return nil
}

func (c *Config) loadPeerIdentity() error {
	key := os.Getenv("PEER_ID")
	if key == "" {
		return errors.New("PEER_ID (your private key) is required; create one with --genkey")
	}
	priv, err := cryptoutils.DecodeBase64Key(key)
	if err != nil {
		return fmt.Errorf("PEER_ID is not a valid private key: %w", err)
	}
	c.PeerPrivKey = priv

	if v := os.Getenv("RELAY_PEER_ID"); v != "" {
		id, err := peer.Decode(v)
		if err != nil {
			return fmt.Errorf("RELAY_PEER_ID is not a valid peer ID: %w", err)
		}
		c.RelayID = id
		return nil
	}
	// Older setups put the relay's private key in RELAY_ID and derived the ID from it.
	if v := os.Getenv("RELAY_ID"); v != "" {
		relayKey, err := cryptoutils.DecodeBase64Key(v)
		if err != nil {
			return fmt.Errorf("RELAY_ID is not a valid private key: %w", err)
		}
		id, err := peer.IDFromPrivateKey(relayKey)
		if err != nil {
			return fmt.Errorf("RELAY_ID: %w", err)
		}
		c.RelayID = id
		c.Warnings = append(c.Warnings,
			"RELAY_ID holds the relay's private key; peers only need its public ID. Set RELAY_PEER_ID="+id.String()+" and remove RELAY_ID from this peer's configuration")
		return nil
	}
	return errors.New("RELAY_PEER_ID (the relay's peer ID, printed when the relay starts) is required")
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
		"/ip4/0.0.0.0/tcp/" + c.RelayPort,
		"/ip4/0.0.0.0/udp/" + c.RelayPort + "/quic-v1",
	}
}
