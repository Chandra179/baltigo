package config

import (
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/Chandra179/sabaody/cryptoutils"

	"github.com/libp2p/go-libp2p/core/peer"
)

var envKeys = []string{
	"PEER_ID", "RELAY_ID", "RELAY_PEER_ID", "RELAY_IP", "RELAY_TCP_PORT",
	"PEER_ALLOWLIST", "RELAY_ALLOWLIST", "SEND_RECEIPTS", "PEER_DOWNLOAD_DIR",
	"PEER_MAX_FILE_MB", "PEER_DB", "RELAY_DB",
}

// setEnv runs the test in an empty directory (so no .env is found) with exactly
// the given environment variables set.
func setEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, k := range envKeys {
		old, had := os.LookupEnv(k)
		os.Unsetenv(k)
		t.Cleanup(func() {
			if had {
				os.Setenv(k, old)
			} else {
				os.Unsetenv(k)
			}
		})
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func identity(t *testing.T) (string, peer.ID) {
	t.Helper()
	key, id, err := cryptoutils.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	return key, id
}

func TestPeerModeNeedsOnlyThePublicRelayID(t *testing.T) {
	peerKey, _ := identity(t)
	_, relayID := identity(t)
	setEnv(t, map[string]string{
		"PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String(),
		"RELAY_IP": "203.0.113.7", "RELAY_TCP_PORT": "4001",
	})
	c, err := LoadConfig(ModePeer)
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayID != relayID || c.PeerPrivKey == nil || c.RelayPrivKey != nil {
		t.Errorf("config = %+v", c)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", c.Warnings)
	}
	info, err := c.RelayAddrInfo()
	if err != nil || info.ID != relayID || len(info.Addrs) != 2 {
		t.Errorf("RelayAddrInfo = %+v, %v", info, err)
	}
	if info.Addrs[0].String() != "/ip4/203.0.113.7/tcp/4001" || info.Addrs[1].String() != "/ip4/203.0.113.7/udp/4001/quic-v1" {
		t.Errorf("relay addrs = %v", info.Addrs)
	}
}

func TestLegacyRelayKeyStillWorksWithWarning(t *testing.T) {
	peerKey, _ := identity(t)
	relayKey, relayID := identity(t)
	setEnv(t, map[string]string{
		"PEER_ID": peerKey, "RELAY_ID": relayKey,
		"RELAY_IP": "203.0.113.7", "RELAY_TCP_PORT": "4001",
	})
	c, err := LoadConfig(ModePeer)
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayID != relayID {
		t.Errorf("RelayID = %s, want %s", c.RelayID, relayID)
	}
	if c.RelayPrivKey != nil {
		t.Error("a peer must not keep the relay's private key")
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "RELAY_PEER_ID="+relayID.String()) {
		t.Errorf("warnings = %v", c.Warnings)
	}
}

func TestRelayModeNeedsNoPeerKey(t *testing.T) {
	relayKey, relayID := identity(t)
	setEnv(t, map[string]string{
		"RELAY_ID": relayKey, "RELAY_IP": "203.0.113.7", "RELAY_TCP_PORT": "4001",
		"RELAY_ALLOWLIST": relayID.String(), "RELAY_DB": "/data/mb.db",
	})
	c, err := LoadConfig(ModeRelay)
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayID != relayID || c.RelayPrivKey == nil || c.PeerPrivKey != nil {
		t.Errorf("config = %+v", c)
	}
	if len(c.RelayAllowlist) != 1 || c.RelayDB != "/data/mb.db" {
		t.Errorf("allowlist/db = %v %q", c.RelayAllowlist, c.RelayDB)
	}
	if got := c.RelayListenAddrs(); len(got) != 2 || got[0] != "/ip4/0.0.0.0/tcp/4001" || got[1] != "/ip4/0.0.0.0/udp/4001/quic-v1" {
		t.Errorf("listen addrs = %v", got)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	peerKey, _ := identity(t)
	relayKey, relayID := identity(t)
	base := func(extra map[string]string) map[string]string {
		m := map[string]string{"RELAY_IP": "203.0.113.7", "RELAY_TCP_PORT": "4001"}
		maps.Copy(m, extra)
		return m
	}
	tests := []struct {
		name string
		mode string
		env  map[string]string
		want string
	}{
		{"unknown mode", "bogus", base(nil), "unknown mode"},
		{"no relay ip", ModePeer, map[string]string{"RELAY_TCP_PORT": "1", "PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String()}, "RELAY_IP"},
		{"bad port", ModePeer, base(map[string]string{"RELAY_TCP_PORT": "99999", "PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String()}), "RELAY_TCP_PORT"},
		{"non-numeric port", ModePeer, base(map[string]string{"RELAY_TCP_PORT": "abc", "PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String()}), "RELAY_TCP_PORT"},
		{"bad ip", ModePeer, base(map[string]string{"RELAY_IP": "not an ip", "PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String()}), "invalid relay address"},
		{"peer without key", ModePeer, base(map[string]string{"RELAY_PEER_ID": relayID.String()}), "PEER_ID"},
		{"peer with bad key", ModePeer, base(map[string]string{"PEER_ID": "garbage", "RELAY_PEER_ID": relayID.String()}), "PEER_ID is not a valid"},
		{"peer without relay id", ModePeer, base(map[string]string{"PEER_ID": peerKey}), "RELAY_PEER_ID"},
		{"peer with bad relay id", ModePeer, base(map[string]string{"PEER_ID": peerKey, "RELAY_PEER_ID": "nope"}), "RELAY_PEER_ID is not a valid"},
		{"relay without key", ModeRelay, base(nil), "RELAY_ID"},
		{"relay with bad key", ModeRelay, base(map[string]string{"RELAY_ID": "garbage"}), "RELAY_ID is not a valid"},
		{"bad allowlist", ModeRelay, base(map[string]string{"RELAY_ID": relayKey, "RELAY_ALLOWLIST": "xyz"}), "RELAY_ALLOWLIST"},
		{"bad peer allowlist", ModePeer, base(map[string]string{"PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String(), "PEER_ALLOWLIST": "xyz"}), "PEER_ALLOWLIST"},
		{"bad max file size", ModePeer, base(map[string]string{"PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String(), "PEER_MAX_FILE_MB": "0"}), "PEER_MAX_FILE_MB"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setEnv(t, tc.env)
			_, err := LoadConfig(tc.mode)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestOptionalSettings(t *testing.T) {
	peerKey, _ := identity(t)
	_, relayID := identity(t)
	_, friend := identity(t)
	setEnv(t, map[string]string{
		"PEER_ID": peerKey, "RELAY_PEER_ID": relayID.String(),
		"RELAY_IP": "203.0.113.7", "RELAY_TCP_PORT": "4001",
		"SEND_RECEIPTS": "FALSE", "PEER_MAX_FILE_MB": "5", "PEER_DOWNLOAD_DIR": "/tmp/dl",
		"PEER_DB": "/tmp/me.db", "PEER_ALLOWLIST": friend.String(),
	})
	c, err := LoadConfig(ModePeer)
	if err != nil {
		t.Fatal(err)
	}
	if !c.DisableReceipts || c.MaxFileSize != 5<<20 || c.DownloadDir != "/tmp/dl" || c.PeerDB != "/tmp/me.db" ||
		len(c.PeerAllowlist) != 1 || c.PeerAllowlist[0] != friend {
		t.Errorf("config = %+v", c)
	}
}

func TestDotEnvFile(t *testing.T) {
	peerKey, _ := identity(t)
	_, relayID := identity(t)
	setEnv(t, nil) // empty environment, empty directory
	env := "PEER_ID=" + peerKey + "\nRELAY_PEER_ID=" + relayID.String() + "\nRELAY_IP=198.51.100.1\nRELAY_TCP_PORT=9000\n"
	if err := os.WriteFile(".env", []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, k := range []string{"PEER_ID", "RELAY_PEER_ID", "RELAY_IP", "RELAY_TCP_PORT"} {
			os.Unsetenv(k)
		}
	})
	c, err := LoadConfig(ModePeer)
	if err != nil {
		t.Fatal(err)
	}
	if c.RelayID != relayID || c.RelayIP != "198.51.100.1" || c.RelayPort != "9000" {
		t.Errorf("config = %+v", c)
	}
}

func TestMalformedDotEnvIsAnError(t *testing.T) {
	setEnv(t, nil)
	if err := os.WriteFile(".env", []byte("this is not = valid =\n\"unterminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(ModePeer); err == nil || !strings.Contains(err.Error(), ".env") {
		t.Errorf("err = %v, want a .env error", err)
	}
}
