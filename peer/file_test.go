package peer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Chandra179/sabaody/store"

	"github.com/libp2p/go-libp2p/core/peer"
)

type fileEnv struct {
	alice, bob *PeerInfo
	sa, sb     *store.Store
	dir        string // bob's download directory
	received   atomic.Int32
}

// newFileEnv connects Alice and Bob directly. Bob has Alice as a contact unless noContact is set.
func newFileEnv(t *testing.T, noContact bool) *fileEnv {
	t.Helper()
	a, _, sa := newGroupPeer(t)
	b, _, sb := newGroupPeer(t)
	linkPeers(t, a, b)
	env := &fileEnv{alice: a, bob: b, sa: sa, sb: sb, dir: filepath.Join(t.TempDir(), "downloads")}
	b.DownloadDir = env.dir
	b.OnFile = func(peer.ID, string, int64) { env.received.Add(1) }
	if !noContact {
		if err := sb.AddContact("alice", a.Host.ID()); err != nil {
			t.Fatal(err)
		}
	}
	return env
}

func writeTemp(t *testing.T, name string, size int) (string, []byte) {
	t.Helper()
	data := make([]byte, size)
	rand.Read(data)
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, data
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestSendFile(t *testing.T) {
	env := newFileEnv(t, false)
	path, data := writeTemp(t, "report.bin", 3<<20+123)

	if err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), path); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "file callback", func() bool { return env.received.Load() == 1 })

	got, err := os.ReadFile(filepath.Join(env.dir, "report.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("received content differs (err %v, %d bytes)", err, len(got))
	}
	if st, _ := os.Stat(filepath.Join(env.dir, "report.bin")); st.Mode().Perm() != 0o600 {
		t.Errorf("received file mode = %v, want 0600", st.Mode().Perm())
	}
	if names := dirEntries(t, env.dir); len(names) != 1 {
		t.Errorf("download dir = %v, want only the file", names)
	}

	ha, _ := env.sa.History(env.bob.Host.ID(), 0)
	hb, _ := env.sb.History(env.alice.Host.ID(), 0)
	if len(ha) != 1 || ha[0].Incoming || !strings.Contains(ha[0].Text, "[file] sent report.bin") {
		t.Errorf("sender history = %+v", ha)
	}
	if len(hb) != 1 || !hb[0].Incoming || !strings.Contains(hb[0].Text, "[file] received report.bin") {
		t.Errorf("receiver history = %+v", hb)
	}
}

func TestSendFileNeverOverwrites(t *testing.T) {
	env := newFileEnv(t, false)
	path, _ := writeTemp(t, "photo.jpg", 10_000)
	for i := 0; i < 3; i++ {
		if err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), path); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "3 files", func() bool { return env.received.Load() == 3 })
	names := dirEntries(t, env.dir)
	want := map[string]bool{"photo.jpg": true, "photo (1).jpg": true, "photo (2).jpg": true}
	if len(names) != 3 {
		t.Fatalf("files = %v", names)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected file %q", n)
		}
	}
}

func TestFileRefusals(t *testing.T) {
	t.Run("not a contact", func(t *testing.T) {
		env := newFileEnv(t, true)
		path, _ := writeTemp(t, "x.txt", 100)
		err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), path)
		if err == nil || !strings.Contains(err.Error(), "not in the recipient's contacts") {
			t.Errorf("err = %v", err)
		}
		if names := dirEntries(t, env.dir); len(names) != 0 {
			t.Errorf("files written for a stranger: %v", names)
		}
		if h, _ := env.sa.History(env.bob.Host.ID(), 0); len(h) != 0 {
			t.Errorf("failed send was recorded: %+v", h)
		}
	})
	t.Run("too large", func(t *testing.T) {
		env := newFileEnv(t, false)
		env.bob.MaxFileSize = 1000
		path, _ := writeTemp(t, "big.bin", 5000)
		err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), path)
		if err == nil || !strings.Contains(err.Error(), "too large") {
			t.Errorf("err = %v", err)
		}
		if names := dirEntries(t, env.dir); len(names) != 0 {
			t.Errorf("files written: %v", names)
		}
	})
	t.Run("not on allowlist", func(t *testing.T) {
		env := newFileEnv(t, false)
		_, other := newIdentity(t)
		env.bob.allow = newAllow(other)
		path, _ := writeTemp(t, "x.txt", 100)
		if err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), path); err == nil {
			t.Error("file from non-allowlisted peer accepted")
		}
	})
	t.Run("sender side checks", func(t *testing.T) {
		env := newFileEnv(t, false)
		empty, _ := writeTemp(t, "empty", 0)
		if err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), empty); err == nil {
			t.Error("empty file sent")
		}
		if err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), t.TempDir()); err == nil {
			t.Error("directory sent")
		}
		if err := env.alice.SendFile(t.Context(), env.bob.Host.ID(), "/nonexistent/file"); err == nil {
			t.Error("missing file sent")
		}
	})
}

// offerFor builds a validly signed offer for data under an arbitrary name.
func offerFor(t *testing.T, from *PeerInfo, to peer.ID, name string, data []byte) *FileOffer {
	t.Helper()
	priv := from.Host.Peerstore().PrivKey(from.Host.ID())
	o, err := NewSignedFileOffer(priv, from.Host.ID(), to, name, int64(len(data)), sha256.Sum256(data))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestFileHostileNames(t *testing.T) {
	env := newFileEnv(t, false)
	root := filepath.Dir(env.dir)
	data := []byte("payload")
	for _, evil := range []string{"../../escape.txt", "..\\..\\escape2.txt", "/etc/passwd-copy", ".hidden", "a/b/c.txt", "..", ""} {
		before := env.received.Load()
		o := offerFor(t, env.alice, env.bob.Host.ID(), evil, data)
		if err := env.alice.transferFile(t.Context(), env.bob.Host.ID(), o, bytes.NewReader(data)); err != nil {
			t.Fatalf("%q: %v", evil, err)
		}
		waitFor(t, "file callback", func() bool { return env.received.Load() == before+1 })
	}
	// every file landed inside the download directory, none next to it
	for _, n := range dirEntries(t, root) {
		if n != "downloads" {
			t.Errorf("file escaped the download dir: %s", n)
		}
	}
	got := dirEntries(t, env.dir)
	want := map[string]bool{"escape.txt": true, "escape2.txt": true, "passwd-copy": true, "hidden": true, "c.txt": true, "file": true, "file (1)": true}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected name %q (all: %v)", n, got)
		}
	}
	if len(got) != 7 {
		t.Errorf("files = %v", got)
	}
}

func TestFileIntegrityAndAuthenticity(t *testing.T) {
	env := newFileEnv(t, false)
	data := bytes.Repeat([]byte("abc"), 5000)
	bobID := env.bob.Host.ID()

	t.Run("wrong checksum", func(t *testing.T) {
		o := offerFor(t, env.alice, bobID, "bad.bin", data)
		corrupted := append([]byte(nil), data...)
		corrupted[100] ^= 0xff
		err := env.alice.transferFile(t.Context(), bobID, o, bytes.NewReader(corrupted))
		if err == nil || !strings.Contains(err.Error(), "checksum") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("truncated transfer", func(t *testing.T) {
		o := offerFor(t, env.alice, bobID, "short.bin", data)
		if err := env.alice.transferFile(t.Context(), bobID, o, bytes.NewReader(data[:1000])); err == nil {
			t.Error("truncated transfer reported success")
		}
	})
	t.Run("tampered offer", func(t *testing.T) {
		o := offerFor(t, env.alice, bobID, "ok.bin", data)
		o.Name = "evil.exe"
		err := env.alice.transferFile(t.Context(), bobID, o, bytes.NewReader(data))
		if err == nil || !strings.Contains(err.Error(), "invalid offer") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("offer meant for someone else", func(t *testing.T) {
		_, other := newIdentity(t)
		o := offerFor(t, env.alice, other, "ok.bin", data)
		if err := env.alice.transferFile(t.Context(), bobID, o, bytes.NewReader(data)); err == nil {
			t.Error("offer addressed to another peer accepted")
		}
	})
	t.Run("offer signed by another key", func(t *testing.T) {
		mallory := newTestPeer(t)
		o := offerFor(t, mallory, bobID, "ok.bin", data) // From = mallory
		o.From = env.alice.Host.ID().String()            // claims to be Alice
		if err := env.alice.transferFile(t.Context(), bobID, o, bytes.NewReader(data)); err == nil {
			t.Error("forged offer accepted")
		}
	})

	// none of the failed transfers may leave anything behind, including .part files
	waitFor(t, "cleanup", func() bool { return len(dirEntries(t, env.dir)) == 0 })
	if env.received.Load() != 0 {
		t.Errorf("OnFile called %d times for failed transfers", env.received.Load())
	}
}

func TestSafeFileName(t *testing.T) {
	long := strings.Repeat("é", 200) + ".txt"
	for in, want := range map[string]string{
		"report.pdf":          "report.pdf",
		"../../etc/passwd":    "passwd",
		"C:\\Users\\x\\a.txt": "a.txt",
		"  spaced  ":          "spaced",
		"...":                 "file",
		"":                    "file",
		"bad\x00name\n.txt":   "badname.txt",
	} {
		if got := safeFileName(in); got != want {
			t.Errorf("safeFileName(%q) = %q, want %q", in, got, want)
		}
	}
	got := safeFileName(long)
	if len(got) > maxFileNameLen || !strings.HasSuffix(got, ".txt") || !strings.HasPrefix(got, "é") {
		t.Errorf("long name = %q (%d bytes)", got, len(got))
	}
}
