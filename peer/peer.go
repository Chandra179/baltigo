package peer

import (
	"fmt"
	"log"
	"p2p/access"
	"sync/atomic"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	rhost "github.com/libp2p/go-libp2p/p2p/host/routed"
	ma "github.com/multiformats/go-multiaddr"
)

type PeerInfo struct {
	RoutedHost rhost.RoutedHost
	PeerID     peer.ID
	Host       host.Host
	PeerStore  *PeerStore

	// OnMessage is called for each verified incoming message.
	OnMessage func(from peer.ID, text string)

	replay *replayGuard
	seq    atomic.Uint64
}

func InitPeerHost(peerPrivKey crypto.PrivKey, allowlist []peer.ID, relay peer.AddrInfo) (*PeerInfo, error) {
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
		libp2p.ConnectionGater(access.NewGater(allowlist, relay.ID)),
		libp2p.EnableHolePunching(),
		libp2p.NATPortMap(),
		libp2p.EnableRelay(),
		// Reserves relay slots and advertises /p2p-circuit addresses when we are behind a NAT.
		libp2p.EnableAutoRelayWithStaticRelays([]peer.AddrInfo{relay}),
	)
	if err != nil {
		ps.Close()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}
	p := PeerInfo{Host: h, PeerStore: ps}
	p.OnMessage = func(from peer.ID, text string) {
		fmt.Printf("[%s] %s\n", from, text)
	}
	p.ChatHandler()
	log.Printf("Peer initialized - ID: %s, Protocols: %v", h.ID(), h.Mux().Protocols())

	return &p, nil
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
