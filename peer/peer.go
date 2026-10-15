package peer

import (
	"context"
	"fmt"
	"log"
	"p2p/access"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	rhost "github.com/libp2p/go-libp2p/p2p/host/routed"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"
)

type PeerInfo struct {
	RoutedHost rhost.RoutedHost
	PeerID     peer.ID
	Host       host.Host
	PeerStore  *PeerStore
}

func InitPeerHost(peerPrivKey crypto.PrivKey, allowlist []peer.ID, relayID peer.ID) (*PeerInfo, error) {
	if peerPrivKey == nil {
		return nil, fmt.Errorf("private key is required")
	}
	ps, err := NewPeerStore()
	if err != nil {
		return nil, err
	}
	// TODO: configure ip and port for listen address
	h, err := libp2p.New(
		libp2p.Identity(peerPrivKey),
		libp2p.ConnectionGater(access.NewGater(allowlist, relayID)),
		libp2p.EnableHolePunching(),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
	)
	if err != nil {
		ps.Close()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}
	p := PeerInfo{Host: h, PeerStore: ps}
	p.ChatHandler()
	log.Printf("Peer initialized - ID: %s, Protocols: %v", h.ID(), h.Mux().Protocols())

	return &p, nil
}

func (p *PeerInfo) ConnectAndReserveRelay(relayID peer.ID, relayIP string, relayPort string) error {
	if relayIP == "" || relayPort == "" {
		return fmt.Errorf("relay IP and port are required")
	}
	addr, err := ma.NewMultiaddr(fmt.Sprintf("/ip4/%s/tcp/%s", relayIP, relayPort))
	if err != nil {
		return fmt.Errorf("invalid relay address: %w", err)
	}
	addrInfo := peer.AddrInfo{
		ID:    relayID,
		Addrs: []ma.Multiaddr{addr},
	}
	if err := p.Host.Connect(context.Background(), addrInfo); err != nil {
		return fmt.Errorf("failed to connect to relay: %w", err)
	}
	_, err = client.Reserve(context.Background(), p.Host, addrInfo)
	if err != nil {
		return fmt.Errorf("failed to reserve relay: %w", err)
	}
	log.Printf("Successfully connected to relay: %s", relayID)
	return nil
}

func (p *PeerInfo) Ping(id peer.ID, addr string) error {
	if addr == "" {
		return fmt.Errorf("address is required")
	}

	maddr, err := ma.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("invalid multiaddr: %w", err)
	}

	canDial := p.Host.Network().CanDial(id, maddr)
	log.Printf("Can dial %s at %s: %v", id, addr, canDial)
	return nil
}
