package mailbox

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Chandra179/sabaody/access"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func setup(t *testing.T, allow access.Allowlist) (*Server, host.Host) {
	t.Helper()
	srv, err := NewServer(filepath.Join(t.TempDir(), "mb.db"), allow)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	relay := newHost(t)
	srv.Register(relay)
	return srv, relay
}

func connect(t *testing.T, from, to host.Host) {
	t.Helper()
	if err := from.Connect(t.Context(), peer.AddrInfo{ID: to.ID(), Addrs: to.Addrs()}); err != nil {
		t.Fatal(err)
	}
}

func collect(t *testing.T, h, relay host.Host) [][]byte {
	t.Helper()
	var got [][]byte
	_, err := Fetch(t.Context(), h, relay.ID(), func(b []byte) bool {
		got = append(got, b)
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPutFetchAck(t *testing.T) {
	_, relay := setup(t, nil)
	alice, bob, carol := newHost(t), newHost(t), newHost(t)
	for _, h := range []host.Host{alice, bob, carol} {
		connect(t, h, relay)
	}

	for _, msg := range []string{"first", "second"} {
		if err := Put(t.Context(), alice, relay.ID(), bob.ID(), []byte(msg)); err != nil {
			t.Fatal(err)
		}
	}
	// only the addressee can fetch
	if got := collect(t, carol, relay); len(got) != 0 {
		t.Fatalf("carol got %d messages", len(got))
	}
	got := collect(t, bob, relay)
	if len(got) != 2 || string(got[0]) != "first" || string(got[1]) != "second" {
		t.Fatalf("bob got %q", got)
	}
	// acknowledged messages are gone
	if got := collect(t, bob, relay); len(got) != 0 {
		t.Errorf("messages still present after ack: %q", got)
	}
}

func TestUnackedMessagesStay(t *testing.T) {
	_, relay := setup(t, nil)
	alice, bob := newHost(t), newHost(t)
	connect(t, alice, relay)
	connect(t, bob, relay)
	Put(t.Context(), alice, relay.ID(), bob.ID(), []byte("keep me"))

	n, err := Fetch(t.Context(), bob, relay.ID(), func([]byte) bool { return false })
	if err != nil || n != 0 {
		t.Fatalf("fetch: %d, %v", n, err)
	}
	if got := collect(t, bob, relay); len(got) != 1 || string(got[0]) != "keep me" {
		t.Errorf("unacked message lost: %q", got)
	}
}

func TestLimits(t *testing.T) {
	_, relay := setup(t, nil)
	alice, bob := newHost(t), newHost(t)
	connect(t, alice, relay)

	if err := Put(t.Context(), alice, relay.ID(), bob.ID(), bytes.Repeat([]byte("x"), MaxBlob+1)); err == nil || !strings.Contains(err.Error(), ErrTooLarge.Error()) {
		t.Errorf("oversized: %v", err)
	}
	if err := Put(t.Context(), alice, relay.ID(), bob.ID(), nil); err == nil {
		t.Error("empty blob accepted")
	}
	for i := range MaxPerRecipient {
		if err := Put(t.Context(), alice, relay.ID(), bob.ID(), []byte("m")); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if err := Put(t.Context(), alice, relay.ID(), bob.ID(), []byte("m")); err == nil || !strings.Contains(err.Error(), ErrFull.Error()) {
		t.Errorf("full mailbox: %v", err)
	}
}

func TestAllowlist(t *testing.T) {
	alice, bob, mallory := newHost(t), newHost(t), newHost(t)
	_, relay := setup(t, access.NewAllowlist(alice.ID(), bob.ID()))
	for _, h := range []host.Host{alice, bob, mallory} {
		connect(t, h, relay)
	}

	if err := Put(t.Context(), mallory, relay.ID(), bob.ID(), []byte("spam")); err == nil {
		t.Error("unlisted sender accepted")
	}
	if err := Put(t.Context(), alice, relay.ID(), mallory.ID(), []byte("hi")); err == nil {
		t.Error("unlisted recipient accepted")
	}
	if _, err := Fetch(t.Context(), mallory, relay.ID(), func([]byte) bool { return true }); err == nil {
		t.Error("unlisted peer could fetch")
	}
	if err := Put(t.Context(), alice, relay.ID(), bob.ID(), []byte("ok")); err != nil {
		t.Errorf("listed peers rejected: %v", err)
	}
}

func TestExpiry(t *testing.T) {
	srv, relay := setup(t, nil)
	alice, bob := newHost(t), newHost(t)
	connect(t, alice, relay)
	connect(t, bob, relay)
	Put(t.Context(), alice, relay.ID(), bob.ID(), []byte("old"))

	srv.now = func() time.Time { return time.Now().Add(TTL + time.Hour) }
	if got := collect(t, bob, relay); len(got) != 0 {
		t.Errorf("expired message delivered: %q", got)
	}

	srv.now = time.Now
	Put(t.Context(), alice, relay.ID(), bob.ID(), []byte("fresh"))
	srv.now = func() time.Time { return time.Now().Add(2 * TTL) }
	if err := srv.prune(); err != nil {
		t.Fatal(err)
	}
	srv.now = time.Now
	if got := collect(t, bob, relay); len(got) != 0 {
		t.Errorf("pruned message delivered: %q", got)
	}
}

func TestRelayUnreachable(t *testing.T) {
	alice, ghost := newHost(t), newHost(t)
	if err := Put(t.Context(), alice, ghost.ID(), alice.ID(), []byte("x")); err == nil {
		t.Error("expected error when relay is unreachable")
	} else if errors.Is(err, ErrFull) {
		t.Error("wrong error")
	}
}

// Deleting while iterating must not skip neighbouring entries.
func TestExpiryDeletesEveryEntry(t *testing.T) {
	for _, viaPrune := range []bool{false, true} {
		srv, relay := setup(t, nil)
		alice, bob := newHost(t), newHost(t)
		connect(t, alice, relay)
		connect(t, bob, relay)
		for i := range 6 {
			Put(t.Context(), alice, relay.ID(), bob.ID(), []byte{byte('a' + i)})
		}
		srv.now = func() time.Time { return time.Now().Add(TTL + time.Hour) }
		if viaPrune {
			if err := srv.prune(); err != nil {
				t.Fatal(err)
			}
		} else {
			collect(t, bob, relay)
		}
		srv.now = time.Now // if any entry survived it is "unexpired" again
		if got := collect(t, bob, relay); len(got) != 0 {
			t.Errorf("viaPrune=%v: %d expired entries were not deleted", viaPrune, len(got))
		}
	}
}
