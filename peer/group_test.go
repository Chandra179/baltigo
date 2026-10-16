package peer

import (
	"p2p/access"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"p2p/store"

	"github.com/libp2p/go-libp2p/core/peer"
)

type groupMsg struct {
	group string
	from  peer.ID
	text  string
}

type inbox struct {
	mu   sync.Mutex
	msgs []groupMsg
}

func (b *inbox) add(g string, f peer.ID, t string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, groupMsg{g, f, t})
}

func (b *inbox) all() []groupMsg {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]groupMsg(nil), b.msgs...)
}

func newGroupPeer(t *testing.T) (*PeerInfo, *inbox, *store.Store) {
	t.Helper()
	p := newTestPeer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "db"), p.Host.Peerstore().PrivKey(p.Host.ID()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p.Store = st
	in := &inbox{}
	p.OnGroupMessage = in.add
	t.Cleanup(p.Close)
	return p, in, st
}

func linkPeers(t *testing.T, ps ...*PeerInfo) {
	t.Helper()
	for i, a := range ps {
		for _, b := range ps[i+1:] {
			if err := a.Host.Connect(t.Context(), peer.AddrInfo{ID: b.Host.ID(), Addrs: b.Host.Addrs()}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func join(t *testing.T, p *PeerInfo, name, secret string) {
	t.Helper()
	if err := p.JoinGroup(t.Context(), name, secret); err != nil {
		t.Fatal(err)
	}
}

// waitMembers waits until p sees n other members in the group. GossipSub drops
// messages sent to a peer whose outbound stream is still being negotiated, even
// after its subscription is known, so give the streams a moment to settle.
func waitMembers(t *testing.T, p *PeerInfo, name string, n int) {
	t.Helper()
	waitFor(t, "group members", func() bool { return p.Groups()[name] >= n })
	time.Sleep(500 * time.Millisecond)
}

func TestGroupChat(t *testing.T) {
	a, ina, sa := newGroupPeer(t)
	b, inb, sb := newGroupPeer(t)
	c, inc, _ := newGroupPeer(t)
	linkPeers(t, a, b, c)

	join(t, a, "team", "s3cret")
	join(t, b, "team", "s3cret")
	join(t, c, "team", "different-secret") // same name, wrong secret: lands on another topic
	waitMembers(t, a, "team", 1)
	waitMembers(t, b, "team", 1)

	n, err := a.SendGroup("team", "hello team")
	if err != nil || n != 1 {
		t.Fatalf("SendGroup: members=%d err=%v", n, err)
	}
	waitFor(t, "b to receive", func() bool { return len(inb.all()) == 1 })
	got := inb.all()[0]
	if got.group != "team" || got.from != a.Host.ID() || got.text != "hello team" {
		t.Errorf("b got %+v", got)
	}
	time.Sleep(300 * time.Millisecond)
	if len(ina.all()) != 0 {
		t.Errorf("sender received its own message: %+v", ina.all())
	}
	if len(inc.all()) != 0 {
		t.Errorf("peer with the wrong secret received: %+v", inc.all())
	}

	// both sides keep a history, with the author recorded
	ha, _ := sa.GroupHistory("team", 0)
	hb, _ := sb.GroupHistory("team", 0)
	if len(ha) != 1 || ha[0].Incoming || ha[0].Author != a.Host.ID() {
		t.Errorf("sender history = %+v", ha)
	}
	if len(hb) != 1 || !hb[0].Incoming || hb[0].Author != a.Host.ID() || hb[0].Text != "hello team" {
		t.Errorf("receiver history = %+v", hb)
	}

	// replies flow the other way
	if _, err := b.SendGroup("team", "hi back"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a to receive", func() bool { return len(ina.all()) == 1 })
	if ina.all()[0].from != b.Host.ID() {
		t.Errorf("a got %+v", ina.all()[0])
	}

	peers, _ := a.GroupPeers("team")
	if len(peers) != 1 || peers[0] != b.Host.ID() {
		t.Errorf("GroupPeers = %v", peers)
	}
}

func TestGroupRejectsBadMessages(t *testing.T) {
	a, _, _ := newGroupPeer(t)
	b, inb, _ := newGroupPeer(t)
	linkPeers(t, a, b)
	join(t, a, "team", "pw")
	join(t, b, "team", "pw")
	waitMembers(t, a, "team", 1)

	ga := a.groups["team"]
	publish := func(data []byte) {
		t.Helper()
		if err := ga.topic.Publish(t.Context(), data); err != nil {
			t.Fatal(err)
		}
	}

	publish([]byte("not encrypted at all"))
	publish(make([]byte, 100))

	// valid ciphertext, but too old: a captured message replayed long after
	old, _ := ga.seal("stale", time.Now().Add(-time.Hour))
	publish(old)

	// the same ciphertext published twice is only delivered once
	fresh, _ := ga.seal("once", time.Now())
	publish(fresh)
	publish(fresh)

	waitFor(t, "valid message", func() bool { return len(inb.all()) >= 1 })
	time.Sleep(500 * time.Millisecond)
	msgs := inb.all()
	if len(msgs) != 1 || msgs[0].text != "once" {
		t.Errorf("delivered %+v, want exactly one message \"once\"", msgs)
	}
}

func TestGroupLeaveAndErrors(t *testing.T) {
	a, _, _ := newGroupPeer(t)
	b, inb, _ := newGroupPeer(t)
	linkPeers(t, a, b)

	for _, bad := range []string{"", "has space", "way-too-long-name-way-too-long-name-x", "semi;colon"} {
		if err := a.JoinGroup(t.Context(), bad, ""); err != ErrBadGroupName {
			t.Errorf("JoinGroup(%q) = %v", bad, err)
		}
	}
	join(t, a, "g", "x")
	if err := a.JoinGroup(t.Context(), "g", "x"); err != ErrAlreadyJoined {
		t.Errorf("second join: %v", err)
	}
	if _, err := a.SendGroup("nope", "hi"); err != ErrNotJoined {
		t.Errorf("send to unjoined group: %v", err)
	}
	if _, err := a.SendGroup("g", ""); err == nil {
		t.Error("empty message accepted")
	}

	join(t, b, "g", "x")
	waitMembers(t, a, "g", 1)
	if err := b.LeaveGroup("g"); err != nil {
		t.Fatal(err)
	}
	if err := b.LeaveGroup("g"); err != ErrNotJoined {
		t.Errorf("leave twice: %v", err)
	}
	waitFor(t, "b to disappear from a's view", func() bool { return a.Groups()["g"] == 0 })
	a.SendGroup("g", "anyone?")
	time.Sleep(300 * time.Millisecond)
	if len(inb.all()) != 0 {
		t.Errorf("message delivered after leaving: %+v", inb.all())
	}
}

func TestGroupAllowlist(t *testing.T) {
	a, _, _ := newGroupPeer(t)
	b, inb, _ := newGroupPeer(t)
	linkPeers(t, a, b)
	join(t, a, "g", "x")
	join(t, b, "g", "x")
	waitMembers(t, a, "g", 1)

	// b only accepts group messages from a peer on its allowlist; a is not on it
	_, other := newIdentity(t)
	b.allow = newAllow(other)
	a.SendGroup("g", "blocked")
	time.Sleep(500 * time.Millisecond)
	if len(inb.all()) != 0 {
		t.Errorf("message from non-allowlisted peer delivered: %+v", inb.all())
	}
}

func TestDeriveGroup(t *testing.T) {
	t1, _, err := deriveGroup("team", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t2, _, _ := deriveGroup("team", "pw")
	t3, _, _ := deriveGroup("team", "other")
	t4, _, _ := deriveGroup("other", "pw")
	if t1 != t2 {
		t.Error("derivation is not deterministic")
	}
	if t1 == t3 || t1 == t4 {
		t.Error("different secret or name gave the same topic")
	}
	for _, topic := range []string{t1, t3, t4} {
		if len(topic) != len("p2pchat/group/")+32 {
			t.Errorf("unexpected topic %q", topic)
		}
	}
}

func newAllow(ids ...peer.ID) access.Allowlist { return access.NewAllowlist(ids...) }
