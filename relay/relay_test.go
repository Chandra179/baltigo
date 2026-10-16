package relay

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Chandra179/sabaody/config"
	"github.com/Chandra179/sabaody/mailbox"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
)

// freePort returns a port that is currently free for both TCP and UDP.
func freePort(t *testing.T) string {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		u, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		u.Close()
		return strconv.Itoa(port)
	}
	t.Fatal("no free port")
	return ""
}

func newPeer(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func startRelay(t *testing.T, allow ...peer.ID) (*Relay, peer.AddrInfo) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := peer.IDFromPrivateKey(priv)
	cfg := &config.Config{
		RelayIP:        "127.0.0.1",
		RelayPort:      freePort(t),
		RelayID:        id,
		RelayPrivKey:   priv,
		RelayAllowlist: allow,
		RelayDB:        filepath.Join(t.TempDir(), "mailbox.db"),
	}
	r, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	info, err := cfg.RelayAddrInfo()
	if err != nil {
		t.Fatal(err)
	}
	return r, info
}

func TestRelayAdvertisesTCPAndQUIC(t *testing.T) {
	r, info := startRelay(t)
	if r.Host.ID() != info.ID {
		t.Errorf("relay ID = %s, want %s", r.Host.ID(), info.ID)
	}
	var tcp, quic bool
	for _, a := range r.Host.Addrs() {
		s := a.String()
		tcp = tcp || strings.Contains(s, "/tcp/")
		quic = quic || strings.Contains(s, "/quic-v1")
	}
	if !tcp || !quic {
		t.Errorf("relay addresses %v: tcp=%v quic=%v", r.Host.Addrs(), tcp, quic)
	}
}

// Guards the ForceReachabilityPublic setting: without it the relay service never
// starts on a node AutoNAT cannot reach (like localhost) and reservations fail.
func TestRelayAcceptsReservationsFromAllowedPeers(t *testing.T) {
	a, b := newPeer(t), newPeer(t)
	_, info := startRelay(t, a.ID())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	for name, h := range map[string]host.Host{"allowed": a, "unlisted": b} {
		if err := h.Connect(ctx, info); err != nil {
			t.Fatalf("%s peer cannot connect: %v", name, err)
		}
	}
	if _, err := client.Reserve(ctx, a, info); err != nil {
		t.Errorf("allowed peer could not reserve a slot: %v", err)
	}
	if _, err := client.Reserve(ctx, b, info); err == nil {
		t.Error("peer outside RELAY_ALLOWLIST got a reservation")
	}
}

func TestRelayServesMailbox(t *testing.T) {
	a, b := newPeer(t), newPeer(t)
	_, info := startRelay(t, a.ID(), b.ID())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, h := range []host.Host{a, b} {
		if err := h.Connect(ctx, info); err != nil {
			t.Fatal(err)
		}
	}
	if err := mailbox.Put(ctx, a, info.ID, b.ID(), []byte("sealed blob")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var got []string
	if _, err := mailbox.Fetch(ctx, b, info.ID, func(blob []byte) bool {
		got = append(got, string(blob))
		return true
	}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 1 || got[0] != "sealed blob" {
		t.Errorf("fetched %q", got)
	}
}

func TestRelayServesPrivateDHT(t *testing.T) {
	_, info := startRelay(t)
	h := newPeer(t)
	d, err := dht.New(h, dht.Mode(dht.ModeClient), dht.ProtocolPrefix(config.DHTProtocolPrefix), dht.BootstrapPeers(info))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := h.Connect(ctx, info); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for d.RoutingTable().Size() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("relay did not join the client's routing table: it is not serving the private DHT")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	priv, _, _ := crypto.GenerateEd25519Key(nil)
	id, _ := peer.IDFromPrivateKey(priv)
	cfg := &config.Config{RelayIP: "127.0.0.1", RelayPort: freePort(t), RelayID: id, RelayPrivKey: priv,
		RelayDB: filepath.Join(t.TempDir(), "mailbox.db")}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, cfg) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}
