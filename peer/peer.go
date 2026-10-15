package peer

import (
	"fmt"
	"log"
	"p2p/access"
	"p2p/store"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	rhost "github.com/libp2p/go-libp2p/p2p/host/routed"
	ma "github.com/multiformats/go-multiaddr"
)

type PeerInfo struct {
	RoutedHost rhost.RoutedHost
	PeerID     peer.ID
	Host       host.Host
	PeerStore  *PeerStore
	// Store persists contacts and message history. May be nil.
	Store *store.Store

	// Relay is the relay (and mailbox) used for offline delivery. Zero value disables it.
	Relay peer.AddrInfo

	// OnMessage is called for each verified incoming message.
	OnMessage func(from peer.ID, text string)

	allow  access.Allowlist
	replay *replayGuard
	seq    atomic.Uint64
}

func InitPeerHost(peerPrivKey crypto.PrivKey, allowlist []peer.ID, relay peer.AddrInfo, st *store.Store) (*PeerInfo, error) {
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
	// Keep the relay's addresses so we can redial it for the mailbox after a disconnect.
	h.Peerstore().AddAddrs(relay.ID, relay.Addrs, peerstore.PermanentAddrTTL)
	p := PeerInfo{Host: h, PeerStore: ps, Store: st, Relay: relay, allow: access.NewAllowlist(allowlist...)}
	p.OnMessage = func(from peer.ID, text string) {
		fmt.Printf("[%s] %s\n", p.DisplayName(from), text)
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

// DisplayName returns the contact name for id, or the peer ID if it is unknown.
func (p *PeerInfo) DisplayName(id peer.ID) string {
	if p.Store != nil {
		if name := p.Store.NameOf(id); name != "" {
			return name
		}
	}
	return id.String()
}

func (p *PeerInfo) record(id peer.ID, incoming, queued bool, t time.Time, text string) {
	if p.Store == nil {
		return
	}
	err := p.Store.AppendMessage(store.Entry{Peer: id, Incoming: incoming, Queued: queued, Time: t, Text: text})
	if err != nil {
		log.Printf("Failed to save message: %v", err)
	}
}
