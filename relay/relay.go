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
	listenAddr := fmt.Sprintf("/ip4/0.0.0.0/tcp/%s", cfg.RelayPort)
	advertiseAddr := fmt.Sprintf("/ip4/%s/tcp/%s", cfg.RelayIP, cfg.RelayPort)

	advertiseMA, err := ma.NewMultiaddr(advertiseAddr)
	if err != nil {
		fmt.Printf("Invalid relay advertise address: %v\n", err)
		return
	}

	if len(cfg.RelayAllowlist) == 0 {
		fmt.Println("WARNING: RELAY_ALLOWLIST is empty, the relay is open to everyone")
	}

	r, err := libp2p.New(
		libp2p.Identity(cfg.RelayPrivKey),
		libp2p.ListenAddrStrings(listenAddr),
		libp2p.AddrsFactory(func([]ma.Multiaddr) []ma.Multiaddr {
			return []ma.Multiaddr{advertiseMA}
		}),
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
