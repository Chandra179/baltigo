package access

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestGater(t *testing.T) {
	friend, relay, stranger := newID(t), newID(t), newID(t)

	open := NewGater(nil, relay)
	if !open.InterceptSecured(network.DirInbound, stranger, nil) {
		t.Error("empty allowlist should allow everyone")
	}

	g := NewGater([]peer.ID{friend}, relay)
	for id, want := range map[peer.ID]bool{friend: true, relay: true, stranger: false} {
		if got := g.InterceptSecured(network.DirInbound, id, nil); got != want {
			t.Errorf("inbound %s: got %v want %v", id, got, want)
		}
	}
	if !g.InterceptSecured(network.DirOutbound, stranger, nil) {
		t.Error("outbound dials should not be gated")
	}
}

func TestRelayACL(t *testing.T) {
	a, b, c := newID(t), newID(t), newID(t)
	acl := NewRelayACL([]peer.ID{a, b})
	if !acl.AllowReserve(a, nil) || acl.AllowReserve(c, nil) {
		t.Error("reserve ACL wrong")
	}
	if !acl.AllowConnect(a, nil, b) || acl.AllowConnect(a, nil, c) || acl.AllowConnect(c, nil, a) {
		t.Error("connect ACL wrong")
	}
}

func TestParseList(t *testing.T) {
	id := newID(t)
	got, err := ParseList(" " + id.String() + " ,")
	if err != nil || len(got) != 1 || got[0] != id {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := ParseList("not-a-peer"); err == nil {
		t.Error("expected error for invalid id")
	}
}
