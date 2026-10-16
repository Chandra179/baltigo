package peer

import (
	"bytes"
	"github.com/Chandra179/sabaody/store"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newIdentity(t *testing.T) (crypto.PrivKey, peer.ID) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return priv, id
}

func TestMessageVerify(t *testing.T) {
	priv, alice := newIdentity(t)
	_, bob := newIdentity(t)
	_, eve := newIdentity(t)

	m, err := NewSignedMessage(priv, alice, bob, 1, "hi")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := m.Verify(bob, alice, now, maxClockSkew); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}

	tests := map[string]func() error{
		"tampered text":         func() error { c := *m; c.Text = "evil"; return c.Verify(bob, alice, now, maxClockSkew) },
		"tampered seq":          func() error { c := *m; c.Seq = 2; return c.Verify(bob, alice, now, maxClockSkew) },
		"other recipient":       func() error { return m.Verify(eve, alice, now, maxClockSkew) },
		"wrong connection peer": func() error { return m.Verify(bob, eve, now, maxClockSkew) },
		"stale":                 func() error { return m.Verify(bob, alice, now.Add(time.Hour), maxClockSkew) },
		"bad version":           func() error { c := *m; c.Version = 9; return c.Verify(bob, alice, now, maxClockSkew) },
	}
	for name, fn := range tests {
		if fn() == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestFrameRoundTripAndLimits(t *testing.T) {
	priv, alice := newIdentity(t)
	_, bob := newIdentity(t)
	m, _ := NewSignedMessage(priv, alice, bob, 1, "hello")

	var buf bytes.Buffer
	if err := writeFrame(&buf, m); err != nil {
		t.Fatal(err)
	}
	got, err := readFrame(&buf)
	if err != nil || got.Text != "hello" || !bytes.Equal(got.Sig, m.Sig) {
		t.Fatalf("round trip failed: %v %+v", err, got)
	}

	big, _ := NewSignedMessage(priv, alice, bob, 2, string(make([]byte, maxMessageSize)))
	if err := writeFrame(&bytes.Buffer{}, big); err == nil {
		t.Error("oversized message should not be written")
	}
	// a frame header claiming a huge size must be refused without reading it
	if _, err := readFrame(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0x7f})); err == nil {
		t.Error("oversized frame should be rejected")
	}
}

func TestReplayGuard(t *testing.T) {
	g := newReplayGuard()
	_, id := newIdentity(t)
	if !g.accept(id, 5) || g.accept(id, 5) || g.accept(id, 4) || !g.accept(id, 6) {
		t.Error("replay guard wrong")
	}
}

func newTestPeer(t *testing.T) *PeerInfo {
	t.Helper()
	priv, _ := newIdentity(t)
	h, err := libp2p.New(libp2p.Identity(priv), libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	p := &PeerInfo{Host: h}
	p.ChatHandler()
	return p
}

func TestSendReceiveBetweenHosts(t *testing.T) {
	a, b := newTestPeer(t), newTestPeer(t)
	type recv struct {
		from peer.ID
		text string
	}
	got := make(chan recv, 4)
	b.OnMessage = func(from peer.ID, text string) { got <- recv{from, text} }

	if err := a.Host.Connect(t.Context(), peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two"} {
		if _, err := a.SendSimple(b.Host.ID(), text); err != nil {
			t.Fatal(err)
		}
		select {
		case r := <-got:
			if r.from != a.Host.ID() || r.text != text {
				t.Fatalf("got %+v", r)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("message not received")
		}
	}

	// a replayed (older seq) message must not be delivered
	priv := a.Host.Peerstore().PrivKey(a.Host.ID())
	old, _ := NewSignedMessage(priv, a.Host.ID(), b.Host.ID(), 1, "replay")
	s, err := a.Host.NewStream(t.Context(), b.Host.ID(), CHAT_PROTOCOL)
	if err != nil {
		t.Fatal(err)
	}
	writeFrame(s, old)
	s.Close()
	select {
	case r := <-got:
		t.Fatalf("replayed message delivered: %+v", r)
	case <-time.After(500 * time.Millisecond):
	}
}

func TestMessagesArePersisted(t *testing.T) {
	a, b := newTestPeer(t), newTestPeer(t)
	open := func(p *PeerInfo) *store.Store {
		st, err := store.Open(filepath.Join(t.TempDir(), "db"), p.Host.Peerstore().PrivKey(p.Host.ID()))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		p.Store = st
		return st
	}
	sa, sb := open(a), open(b)
	if err := sb.AddContact("alice", a.Host.ID()); err != nil {
		t.Fatal(err)
	}
	if got := b.DisplayName(a.Host.ID()); got != "alice" {
		t.Errorf("DisplayName = %q, want alice", got)
	}

	got := make(chan struct{}, 1)
	b.OnMessage = func(peer.ID, string) { got <- struct{}{} }
	if err := a.Host.Connect(t.Context(), peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SendSimple(b.Host.ID(), "persist me"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("message not received")
	}

	sent, _ := sa.History(b.Host.ID(), 0)
	if len(sent) != 1 || sent[0].Incoming || sent[0].Text != "persist me" {
		t.Errorf("sender history = %+v", sent)
	}
	recv, _ := sb.History(a.Host.ID(), 0)
	if len(recv) != 1 || !recv[0].Incoming || recv[0].Text != "persist me" {
		t.Errorf("receiver history = %+v", recv)
	}
}
