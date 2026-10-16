package store

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func newKey(t *testing.T) (crypto.PrivKey, peer.ID) {
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

func openTemp(t *testing.T, priv crypto.PrivKey, path string) *Store {
	t.Helper()
	s, err := Open(path, priv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestContacts(t *testing.T) {
	priv, _ := newKey(t)
	s := openTemp(t, priv, filepath.Join(t.TempDir(), "sub", "db"))
	_, alice := newKey(t)
	_, bob := newKey(t)

	if err := s.AddContact("Alice", alice); err != nil {
		t.Fatal(err)
	}
	if err := s.AddContact("alice", bob); !errors.Is(err, ErrNameTaken) {
		t.Errorf("duplicate name: got %v", err)
	}
	if err := s.AddContact("other", alice); !errors.Is(err, ErrPeerExists) {
		t.Errorf("duplicate peer: got %v", err)
	}
	if err := s.AddContact("has space", bob); !errors.Is(err, ErrInvalidName) {
		t.Errorf("invalid name: got %v", err)
	}

	for _, in := range []string{"alice", "ALICE", alice.String()} {
		if id, err := s.Resolve(in); err != nil || id != alice {
			t.Errorf("Resolve(%q) = %v, %v", in, id, err)
		}
	}
	if _, err := s.Resolve("nobody"); err == nil {
		t.Error("expected error for unknown name")
	}
	if s.NameOf(alice) != "Alice" || s.NameOf(bob) != "" {
		t.Errorf("NameOf wrong: %q %q", s.NameOf(alice), s.NameOf(bob))
	}

	if err := s.RemoveContact("alice"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveContact("alice"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove twice: got %v", err)
	}
}

func TestHistoryPersistsAndIsOrdered(t *testing.T) {
	priv, _ := newKey(t)
	path := filepath.Join(t.TempDir(), "db")
	_, alice := newKey(t)
	_, bob := newKey(t)

	s, err := Open(path, priv)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	for i, text := range []string{"one", "two", "three"} {
		e := Entry{Peer: alice, Incoming: i%2 == 0, Time: now.Add(time.Duration(i) * time.Second), Text: text}
		if err := s.AppendMessage(e); err != nil {
			t.Fatal(err)
		}
	}
	s.AppendMessage(Entry{Peer: bob, Text: "for bob"})
	s.Close()

	s = openTemp(t, priv, path) // reopen: data survives
	all, err := s.History(alice, 0)
	if err != nil || len(all) != 3 || all[0].Text != "one" || all[2].Text != "three" || !all[0].Incoming || all[1].Incoming {
		t.Fatalf("history = %+v, err %v", all, err)
	}
	last2, _ := s.History(alice, 2)
	if len(last2) != 2 || last2[0].Text != "two" || last2[1].Text != "three" {
		t.Errorf("limit: %+v", last2)
	}
	if h, _ := s.History(bob, 0); len(h) != 1 || h[0].Text != "for bob" {
		t.Errorf("bob history: %+v", h)
	}
	if !all[0].Time.Equal(now) {
		t.Errorf("time not preserved: %v vs %v", all[0].Time, now)
	}
}

func TestEncryptedAtRest(t *testing.T) {
	priv, _ := newKey(t)
	path := filepath.Join(t.TempDir(), "db")
	_, alice := newKey(t)
	s, _ := Open(path, priv)
	s.AddContact("SecretName", alice)
	s.AppendMessage(Entry{Peer: alice, Text: "top secret text"})
	s.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"SecretName", "top secret text"} {
		if bytesContains(raw, needle) {
			t.Errorf("plaintext %q found in database file", needle)
		}
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Errorf("db perms = %v, want 0600", st.Mode().Perm())
	}

	// a different identity key cannot read the data
	other, _ := newKey(t)
	s2 := openTemp(t, other, path)
	if _, err := s2.History(alice, 0); !errors.Is(err, ErrCannotDecrypt) {
		t.Errorf("history with wrong key: got %v", err)
	}
	if _, err := s2.Contacts(); !errors.Is(err, ErrCannotDecrypt) {
		t.Errorf("contacts with wrong key: got %v", err)
	}
}

func bytesContains(b []byte, s string) bool { return bytes.Contains(b, []byte(s)) }

func TestMarkSeen(t *testing.T) {
	priv, _ := newKey(t)
	s := openTemp(t, priv, filepath.Join(t.TempDir(), "db"))
	_, alice := newKey(t)
	_, bob := newKey(t)

	for _, tc := range []struct {
		id   peer.ID
		seq  uint64
		want bool
	}{{alice, 1, true}, {alice, 1, false}, {alice, 2, true}, {bob, 1, true}, {bob, 1, false}} {
		got, err := s.MarkSeen(tc.id, tc.seq)
		if err != nil || got != tc.want {
			t.Errorf("MarkSeen(%s,%d) = %v, %v; want %v", tc.id, tc.seq, got, err, tc.want)
		}
	}

	// entries older than the retention period are forgotten
	s.pruneSeen(time.Now().Add(seenRetention + time.Hour))
	if got, _ := s.MarkSeen(alice, 1); !got {
		t.Error("old entry was not pruned")
	}
}

func TestMarkDelivered(t *testing.T) {
	priv, _ := newKey(t)
	s := openTemp(t, priv, filepath.Join(t.TempDir(), "db"))
	_, alice := newKey(t)
	s.AppendMessage(Entry{Peer: alice, Seq: 10, Text: "one"})
	s.AppendMessage(Entry{Peer: alice, Seq: 11, Text: "two"})
	s.AppendMessage(Entry{Peer: alice, Seq: 10, Incoming: true, Text: "incoming with same seq"})

	if found, err := s.MarkDelivered(alice, 99); err != nil || found {
		t.Errorf("unknown seq: %v, %v", found, err)
	}
	if found, err := s.MarkDelivered(alice, 10); err != nil || !found {
		t.Fatalf("MarkDelivered: %v, %v", found, err)
	}
	if found, _ := s.MarkDelivered(alice, 10); !found {
		t.Error("second MarkDelivered should still find the entry")
	}

	h, _ := s.History(alice, 0)
	if len(h) != 3 || !h[0].Delivered || h[1].Delivered || h[2].Delivered {
		t.Errorf("history = %+v", h)
	}
	if h[0].Text != "one" || h[2].Text != "incoming with same seq" {
		t.Errorf("order or content changed: %+v", h)
	}
}

// The database key must never change, or existing databases become unreadable.
func TestDeriveKeyKnownAnswer(t *testing.T) {
	k, err := deriveKey([]byte("fixed identity key bytes for a golden test"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "d460505f0cd683b15da65b79ef9844d882e663ae5bb063eaa2012ee03bb022a5"
	if got := hex.EncodeToString(k); got != want {
		t.Errorf("derived key = %s, want %s", got, want)
	}
}
