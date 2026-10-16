// Package relay runs the public node that peers use for NAT traversal,
// bootstrapping the private DHT and offline message delivery.
package relay

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Chandra179/sabaody/access"
	"github.com/Chandra179/sabaody/config"
	"github.com/Chandra179/sabaody/mailbox"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

// Relay is a running relay node: circuit relay v2, the private DHT server and the mailbox.
type Relay struct {
	Host    host.Host
	dht     *dht.IpfsDHT
	mailbox *mailbox.Server
}

// Start brings up the relay described by cfg and returns once it is serving.
func Start(cfg *config.Config) (*Relay, error) {
	info, err := cfg.RelayAddrInfo()
	if err != nil {
		return nil, err
	}

	h, err := libp2p.New(
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
		return nil, fmt.Errorf("failed to create relay: %w", err)
	}
	r := &Relay{Host: h}

	// Serve the private DHT so peers can bootstrap from the relay.
	r.dht, err = dht.New(h, dht.Mode(dht.ModeServer), dht.ProtocolPrefix(config.DHTProtocolPrefix))
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("failed to start DHT: %w", err)
	}

	// Hold sealed messages for peers that are offline.
	dbPath := cfg.RelayDB
	if dbPath == "" {
		dbPath = filepath.Join("relay_db", "mailbox.db")
	}
	r.mailbox, err = mailbox.NewServer(dbPath, access.NewAllowlist(cfg.RelayAllowlist...))
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("failed to start mailbox: %w", err)
	}
	r.mailbox.Register(h)
	return r, nil
}

// Close stops the relay and releases its database.
func (r *Relay) Close() error {
	var errs []error
	if r.mailbox != nil {
		errs = append(errs, r.mailbox.Close())
	}
	if r.dht != nil {
		errs = append(errs, r.dht.Close())
	}
	return errors.Join(append(errs, r.Host.Close())...)
}

// Run starts the relay, prints how peers should reach it and serves until ctx is cancelled.
func Run(ctx context.Context, cfg *config.Config) error {
	if len(cfg.RelayAllowlist) == 0 {
		fmt.Println("WARNING: RELAY_ALLOWLIST is empty, the relay is open to everyone")
	}
	r, err := Start(cfg)
	if err != nil {
		return err
	}
	fmt.Printf("Relay started. Give peers this setting:\n  RELAY_PEER_ID=%s\n", r.Host.ID())
	fmt.Printf("Listening on %v\n", r.Host.Addrs())

	<-ctx.Done()
	fmt.Println("Shutting down relay...")
	return r.Close()
}
