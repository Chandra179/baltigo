package peer

import (
	"bytes"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Chandra179/sabaody/cryptoutils"
	"github.com/Chandra179/sabaody/mailbox"
	"github.com/Chandra179/sabaody/store"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

// offlineSetup returns a relay running a mailbox plus two peers that know the
// relay but not each other.
func offlineSetup(t *testing.T) (alice, bob *PeerInfo, sa, sb *store.Store) {
	t.Helper()
	r, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	srv, err := mailbox.NewServer(filepath.Join(t.TempDir(), "mb.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	srv.Register(r)
	relayInfo := peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}

	setup := func() (*PeerInfo, *store.Store) {
		p := newTestPeer(t)
		p.Relay = relayInfo
		st, err := store.Open(filepath.Join(t.TempDir(), "db"), p.Host.Peerstore().PrivKey(p.Host.ID()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		p.Store = st
		return p, st
	}
	alice, sa = setup()
	bob, sb = setup()
	return
}

func TestOfflineDelivery(t *testing.T) {
	alice, bob, sa, sb := offlineSetup(t)

	// Bob is not reachable by Alice, so the message is queued on the relay.
	before := time.Now().Add(-time.Second)
	queued, err := alice.SendSimple(bob.Host.ID(), "are you there?")
	if err != nil || !queued {
		t.Fatalf("SendSimple: queued=%v err=%v", queued, err)
	}
	if h, _ := sa.History(bob.Host.ID(), 0); len(h) != 1 || !h[0].Queued || h[0].Incoming {
		t.Errorf("sender history = %+v", h)
	}

	var got []string
	bob.OnMessage = func(from peer.ID, text string) {
		if from != alice.Host.ID() {
			t.Errorf("from = %s", from)
		}
		got = append(got, text)
	}
	n, err := bob.FetchOffline(t.Context())
	if err != nil || n != 1 || len(got) != 1 || got[0] != "are you there?" {
		t.Fatalf("FetchOffline: n=%d err=%v got=%v", n, err, got)
	}
	h, _ := sb.History(alice.Host.ID(), 0)
	if len(h) != 1 || !h[0].Incoming || h[0].Text != "are you there?" || h[0].Time.Before(before) {
		t.Errorf("receiver history = %+v", h)
	}

	// Delivered messages are removed from the relay.
	if n, err := bob.FetchOffline(t.Context()); err != nil || n != 0 {
		t.Errorf("second fetch: n=%d err=%v", n, err)
	}
}

func TestOfflineReplayAndForgeryRejected(t *testing.T) {
	alice, bob, _, _ := offlineSetup(t)
	var delivered atomic.Int32
	bob.OnMessage = func(peer.ID, string) { delivered.Add(1) }

	// A genuine message, captured by a malicious relay and re-submitted after delivery.
	priv := alice.Host.Peerstore().PrivKey(alice.Host.ID())
	msg, _ := NewSignedMessage(priv, alice.Host.ID(), bob.Host.ID(), alice.nextSeq(), "once")
	var buf bytes.Buffer
	writeFrame(&buf, msg)
	bobPub, _ := bob.Host.ID().ExtractPublicKey()
	blob, _ := cryptoutils.Seal(buf.Bytes(), bobPub)

	put := func(b []byte) {
		t.Helper()
		if err := mailbox.Put(t.Context(), alice.Host, alice.Relay.ID, bob.Host.ID(), b); err != nil {
			t.Fatal(err)
		}
	}
	fetch := func() {
		t.Helper()
		if _, err := bob.FetchOffline(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	alice.Host.Connect(t.Context(), alice.Relay)

	put(blob)
	fetch()
	put(blob) // replay
	fetch()
	if delivered.Load() != 1 {
		t.Fatalf("delivered %d times, want 1", delivered.Load())
	}

	// Mallory seals a message to Bob that claims to come from Alice but is not signed by her.
	mallory := newTestPeer(t)
	mpriv := mallory.Host.Peerstore().PrivKey(mallory.Host.ID())
	forged, _ := NewSignedMessage(mpriv, alice.Host.ID(), bob.Host.ID(), mallory.nextSeq(), "send me money")
	buf.Reset()
	writeFrame(&buf, forged)
	blob, _ = cryptoutils.Seal(buf.Bytes(), bobPub)
	put(blob)
	fetch()

	// A message that is simply too old is refused.
	old := &Message{Version: messageVersion, From: alice.Host.ID().String(), To: bob.Host.ID().String(),
		Seq: alice.nextSeq(), Timestamp: time.Now().Add(-2 * mailbox.TTL).UnixMilli(), Text: "stale"}
	old.Sig, _ = priv.Sign(old.signedBytes())
	buf.Reset()
	writeFrame(&buf, old)
	blob, _ = cryptoutils.Seal(buf.Bytes(), bobPub)
	put(blob)
	fetch()

	if delivered.Load() != 1 {
		t.Errorf("forged or stale message delivered (count %d)", delivered.Load())
	}
}

func TestLiveMessageThenMailboxDuplicateDropped(t *testing.T) {
	alice, bob, _, _ := offlineSetup(t)
	var delivered atomic.Int32
	bob.OnMessage = func(peer.ID, string) { delivered.Add(1) }
	if err := alice.Host.Connect(t.Context(), peer.AddrInfo{ID: bob.Host.ID(), Addrs: bob.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	priv := alice.Host.Peerstore().PrivKey(alice.Host.ID())
	msg, _ := NewSignedMessage(priv, alice.Host.ID(), bob.Host.ID(), alice.nextSeq(), "dup")
	if err := alice.sendLive(bob.Host.ID(), msg); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for delivered.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	// The same message also reaches the mailbox (e.g. a send that was retried).
	if err := alice.queue(bob.Host.ID(), msg); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.FetchOffline(t.Context()); err != nil {
		t.Fatal(err)
	}
	if delivered.Load() != 1 {
		t.Errorf("delivered %d times, want 1", delivered.Load())
	}
}
