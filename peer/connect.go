package peer

import (
	"context"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

func (p *PeerInfo) Connect(ctx context.Context, peerInfo peer.AddrInfo, relayID peer.ID) error {
	if err := p.Host.Connect(ctx, peerInfo); err == nil {
		log.Printf("Connected directly to %s", peerInfo.ID)
		return nil
	}
	log.Printf("Direct connection to %s failed, trying the relay", peerInfo.ID)

	// If direct fails, try relay connection
	addrStr := fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", relayID, peerInfo.ID)
	targetRelayaddr, err := ma.NewMultiaddr(addrStr)
	if err != nil {
		return fmt.Errorf("invalid relay multiaddr: %w", err)
	}
	targetPeer := peer.AddrInfo{
		ID:    peerInfo.ID,
		Addrs: []ma.Multiaddr{targetRelayaddr},
	}
	if err := p.Host.Connect(ctx, targetPeer); err != nil {
		return fmt.Errorf("relay connect failed: %w", err)
	}
	log.Printf("Connected to %s through the relay", peerInfo.ID)
	return nil
}
