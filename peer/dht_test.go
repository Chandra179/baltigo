package peer

import (
	"testing"

	"p2p/config"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestInitDHTBootstrapsFromRelay(t *testing.T) {
	rpriv, _ := newIdentity(t)
	r, err := libp2p.New(libp2p.Identity(rpriv), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"), libp2p.ForceReachabilityPublic())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := dht.New(r, dht.Mode(dht.ModeServer), dht.ProtocolPrefix(config.DHTProtocolPrefix)); err != nil {
		t.Fatal(err)
	}

	p := newTestPeer(t)
	dm, err := p.InitDHT(t.Context(), p.Host, peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()})
	if err != nil {
		t.Fatal(err)
	}
	if dm.DHT.RoutingTable().Size() != 1 {
		t.Errorf("routing table size = %d, want 1", dm.DHT.RoutingTable().Size())
	}
}
