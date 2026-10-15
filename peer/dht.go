package peer

import (
	"context"
	"fmt"
	"p2p/config"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

type DHTManager struct {
	DHT       *dht.IpfsDHT
	Discovery *discovery.RoutingDiscovery
}

// InitDHT initializes a private Kademlia DHT bootstrapped from the given peer (our relay)
func (p *PeerInfo) InitDHT(ctx context.Context, h host.Host, bootstrap peer.AddrInfo) (*DHTManager, error) {
	kademliaDHT, err := dht.New(h,
		dht.Mode(dht.ModeAuto),
		dht.ProtocolPrefix(config.DHTProtocolPrefix),
		dht.BootstrapPeers(bootstrap),
	)
	if err != nil {
		return nil, err
	}
	if err := h.Connect(ctx, bootstrap); err != nil {
		return nil, fmt.Errorf("failed to connect to bootstrap peer: %w", err)
	}
	if err := kademliaDHT.Bootstrap(ctx); err != nil {
		return nil, err
	}
	return &DHTManager{
		DHT:       kademliaDHT,
		Discovery: discovery.NewRoutingDiscovery(kademliaDHT),
	}, nil
}

// AdvertiseHost makes the host discoverable on the DHT under the given tag
func (d *DHTManager) AdvertiseHost(ctx context.Context, rendezvous string) error {
	ttl, err := d.Discovery.Advertise(ctx, rendezvous)
	if err != nil {
		return err
	}
	fmt.Println("Successfully advertised. TTL:", ttl)
	return nil
}

// FindPeers discovers peers by rendezvous string
func (d *DHTManager) FindPeers(ctx context.Context, rendezvous string) (<-chan peer.AddrInfo, error) {
	peerChan, err := d.Discovery.FindPeers(ctx, rendezvous)
	if err != nil {
		return nil, err
	}
	return peerChan, nil
}
