package relay

import (
	"fmt"
	"p2p/access"
	"p2p/config"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

func RunRelay(cfg *config.Config) {
	info, err := cfg.RelayAddrInfo()
	if err != nil {
		fmt.Printf("%v\n", err)
		return
	}

	if len(cfg.RelayAllowlist) == 0 {
		fmt.Println("WARNING: RELAY_ALLOWLIST is empty, the relay is open to everyone")
	}

	r, err := libp2p.New(
		libp2p.Identity(cfg.RelayPrivKey),
		libp2p.ListenAddrStrings(cfg.RelayListenAddrs()...),
		libp2p.AddrsFactory(func([]ma.Multiaddr) []ma.Multiaddr {
			return info.Addrs
		}),
		// A relay is public by definition; without this the service only starts after AutoNAT confirms it.
		libp2p.ForceReachabilityPublic(),
		libp2p.EnableRelayService(relayv2.WithACL(access.NewRelayACL(cfg.RelayAllowlist))),
	)
	if err != nil {
		fmt.Printf("Failed to create relay: %v\n", err)
		return
	}
	// Serve the private DHT so peers can bootstrap from the relay.
	if _, err = dht.New(r, dht.Mode(dht.ModeServer), dht.ProtocolPrefix(config.DHTProtocolPrefix)); err != nil {
		fmt.Printf("Failed to start DHT: %v\n", err)
		return
	}
	relayinfo := peer.AddrInfo{
		ID:    r.ID(),
		Addrs: r.Addrs(),
	}
	fmt.Println(relayinfo.ID.String())
	fmt.Println(relayinfo.Addrs)
	select {}
}
