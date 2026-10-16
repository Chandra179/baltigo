package peer

import (
	"context"
	"errors"
	"fmt"
	"github.com/Chandra179/sabaody/config"
	"time"

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
	// The bootstrap peer is added to the routing table asynchronously, once identify
	// confirms it speaks our DHT protocol.
	if err := waitForRoutingTable(ctx, kademliaDHT, 10*time.Second); err != nil {
		return nil, err
	}
	if err := kademliaDHT.Bootstrap(ctx); err != nil {
		return nil, err
	}
	// Wait for the routing table to be populated so Advertise and FindPeers have peers to query.
	select {
	case err := <-kademliaDHT.RefreshRoutingTable():
		if err != nil {
			return nil, fmt.Errorf("failed to refresh routing table: %w", err)
		}
	case <-ctx.Done():
		return nil, ctx.Err()
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

func waitForRoutingTable(ctx context.Context, d *dht.IpfsDHT, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for d.RoutingTable().Size() == 0 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("bootstrap peer was not added to the routing table: %w", ctx.Err())
		}
	}
	return nil
}

// DHT returns the private DHT, bootstrapping it from the relay on first use.
func (p *PeerInfo) DHT(ctx context.Context) (*DHTManager, error) {
	p.dhtMu.Lock()
	defer p.dhtMu.Unlock()
	if p.dhtMgr != nil {
		return p.dhtMgr, nil
	}
	if p.Relay.ID == "" {
		return nil, errors.New("no relay configured")
	}
	dm, err := p.InitDHT(ctx, p.Host, p.Relay)
	if err != nil {
		return nil, err
	}
	p.dhtMgr = dm
	return dm, nil
}
