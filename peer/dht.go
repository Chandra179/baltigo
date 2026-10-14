package peer

import (
	"context"
	"fmt"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	discovery "github.com/libp2p/go-libp2p/p2p/discovery/routing"
)

type DHTManager struct {
	DHT       *dht.IpfsDHT
	Discovery *discovery.RoutingDiscovery
}

// InitDHT initializes Kademlia DHT with bootstrap peers
func (p *PeerInfo) InitDHT(ctx context.Context, h host.Host) (*DHTManager, error) {
	kademliaDHT, err := dht.New(h, dht.Mode(dht.ModeAuto))
	if err != nil {
		return nil, err
	}
	dft := dht.DefaultBootstrapPeers
	for _, addr := range dft {
		peerinfo, _ := peer.AddrInfoFromP2pAddr(addr)
		err = h.Connect(ctx, *peerinfo)
		if err != nil {
			fmt.Println("Failed to connect to bootstrap peer:", peerinfo.ID, err)
			continue
		}
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
