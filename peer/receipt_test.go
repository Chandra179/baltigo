package peer

import (
	"bytes"
	"p2p/wire"
	"sync/atomic"
	"testing"
	"time"

	"p2p/cryptoutils"
	"p2p/mailbox"
	"p2p/store"

	"github.com/libp2p/go-libp2p/core/peer"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func delivered(t *testing.T, st *store.Store, to peer.ID) []bool {
	t.Helper()
	h, err := st.History(to, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []bool
	for _, e := range h {
		if !e.Incoming {
			out = append(out, e.Delivered)
		}
	}
	return out
}

func TestReceiptVerify(t *testing.T) {
	priv, bob := newIdentity(t)
	_, alice := newIdentity(t)
	_, eve := newIdentity(t)
	r, err := NewSignedReceipt(priv, bob, alice, 42)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := r.Verify(alice, bob, now, maxClockSkew); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	tests := map[string]func() error{
		"tampered seq":    func() error { c := *r; c.Seq = 43; return c.Verify(alice, bob, now, maxClockSkew) },
		"other recipient": func() error { return r.Verify(eve, bob, now, maxClockSkew) },
		"wrong peer":      func() error { return r.Verify(alice, eve, now, maxClockSkew) },
		"stale":           func() error { return r.Verify(alice, bob, now.Add(time.Hour), maxClockSkew) },
		"bad version":     func() error { c := *r; c.Version = 9; return c.Verify(alice, bob, now, maxClockSkew) },
		"bad kind":        func() error { c := *r; c.Kind = "msg"; return c.Verify(alice, bob, now, maxClockSkew) },
	}
	for name, fn := range tests {
		if fn() == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	// a message signature is not a valid receipt signature (domain separation)
	m, _ := NewSignedMessage(priv, bob, alice, 42, "")
	c := *r
	c.Sig = m.Sig
	if c.Verify(alice, bob, now, maxClockSkew) == nil {
		t.Error("message signature accepted as receipt signature")
	}
}

func TestLiveReceipt(t *testing.T) {
	_, alice, bob, sa, _ := offlineSetup(t)
	var gotReceipt atomic.Uint64
	alice.OnReceipt = func(from peer.ID, seq uint64) {
		if from != bob.Host.ID() {
			t.Errorf("receipt from %s", from)
		}
		gotReceipt.Store(seq)
	}
	if err := alice.Host.Connect(t.Context(), peer.AddrInfo{ID: bob.Host.ID(), Addrs: bob.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	queued, err := alice.SendSimple(bob.Host.ID(), "ping")
	if err != nil || queued {
		t.Fatalf("SendSimple: queued=%v err=%v", queued, err)
	}
	waitFor(t, "delivery receipt", func() bool { d := delivered(t, sa, bob.Host.ID()); return len(d) == 1 && d[0] })
	if gotReceipt.Load() == 0 {
		t.Error("OnReceipt not called")
	}
}

func TestReceiptsCanBeDisabled(t *testing.T) {
	_, alice, bob, sa, _ := offlineSetup(t)
	bob.DisableReceipts = true
	if err := alice.Host.Connect(t.Context(), peer.AddrInfo{ID: bob.Host.ID(), Addrs: bob.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{}, 1)
	bob.OnMessage = func(peer.ID, string) { got <- struct{}{} }
	alice.SendSimple(bob.Host.ID(), "quiet")
	<-got
	time.Sleep(500 * time.Millisecond)
	if d := delivered(t, sa, bob.Host.ID()); len(d) != 1 || d[0] {
		t.Errorf("delivered = %v, want [false]", d)
	}
}

// Both peers are unreachable to each other: the message and its receipt both travel via the mailbox.
func TestOfflineReceiptViaMailbox(t *testing.T) {
	_, alice, bob, sa, _ := offlineSetup(t)
	queued, err := alice.SendSimple(bob.Host.ID(), "see you later")
	if err != nil || !queued {
		t.Fatalf("SendSimple: queued=%v err=%v", queued, err)
	}
	if d := delivered(t, sa, bob.Host.ID()); len(d) != 1 || d[0] {
		t.Fatalf("delivered before receipt = %v", d)
	}
	if n, err := bob.FetchOffline(t.Context()); err != nil || n != 1 {
		t.Fatalf("bob fetch: n=%d err=%v", n, err)
	}
	// Bob's receipt is now waiting for Alice on the relay.
	waitFor(t, "receipt in mailbox", func() bool {
		n, err := alice.FetchOffline(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		_ = n
		d := delivered(t, sa, bob.Host.ID())
		return len(d) == 1 && d[0]
	})
}

func TestForgedReceiptRejected(t *testing.T) {
	_, alice, bob, sa, _ := offlineSetup(t)
	if err := alice.Host.Connect(t.Context(), peer.AddrInfo{ID: bob.Host.ID(), Addrs: bob.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	bob.DisableReceipts = true // so only the forged receipts could mark it delivered
	got := make(chan struct{}, 1)
	bob.OnMessage = func(peer.ID, string) { got <- struct{}{} }
	alice.SendSimple(bob.Host.ID(), "did you get this?")
	<-got
	h, _ := sa.History(bob.Host.ID(), 0)
	seq := h[0].Seq

	mallory := newTestPeer(t)
	mpriv := mallory.Host.Peerstore().PrivKey(mallory.Host.ID())
	// Mallory signs a receipt that claims to be from Bob.
	forged, _ := NewSignedReceipt(mpriv, bob.Host.ID(), alice.Host.ID(), seq)

	// 1. live: Mallory connects to Alice and sends it
	if err := mallory.Host.Connect(t.Context(), peer.AddrInfo{ID: alice.Host.ID(), Addrs: alice.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := mallory.Host.NewStream(t.Context(), alice.Host.ID(), RECEIPT_PROTOCOL)
	if err != nil {
		t.Fatal(err)
	}
	wireWrite(t, s, forged)
	s.Close()

	// 2. via the mailbox, sealed to Alice
	var buf bytes.Buffer
	wireWrite(t, &buf, forged)
	alicePub, _ := alice.Host.ID().ExtractPublicKey()
	blob, _ := cryptoutils.Seal(buf.Bytes(), alicePub)
	if err := mailbox.Put(t.Context(), mallory.Host, alice.Relay.ID, alice.Host.ID(), blob); err != nil {
		// Mallory has to be connected to the relay first
		mallory.Host.Connect(t.Context(), alice.Relay)
		if err := mailbox.Put(t.Context(), mallory.Host, alice.Relay.ID, alice.Host.ID(), blob); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := alice.FetchOffline(t.Context()); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)
	if d := delivered(t, sa, bob.Host.ID()); len(d) != 1 || d[0] {
		t.Errorf("forged receipt marked message delivered: %v", d)
	}
}

func wireWrite(t *testing.T, w interface{ Write([]byte) (int, error) }, v any) {
	t.Helper()
	if err := wire.Write(w, v, maxReceiptSize); err != nil {
		t.Fatal(err)
	}
}
