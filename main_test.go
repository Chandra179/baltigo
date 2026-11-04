package main

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Chandra179/baltigo/config"
	"github.com/Chandra179/baltigo/cryptoutils"
	"github.com/Chandra179/baltigo/relay"
)

var binary string

// TestMain builds the program once; the tests below run it as a user would.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "baltigo-test")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "baltigo")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		_, _ = os.Stderr.Write(out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// runBinary runs the program in an empty directory with only the given
// environment, so no .env file or ambient setting leaks in.
func runBinary(t *testing.T, stdin string, env []string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = t.TempDir()
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + cmd.Dir}, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("program did not exit; stdout so far:\n%s", out.String())
	}
	return out.String(), errb.String(), exit
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func TestGenkey(t *testing.T) {
	out, _, code := runBinary(t, "", nil, "--genkey")
	if code != 0 || !strings.Contains(out, "Private key") || !strings.Contains(out, "12D3KooW") {
		t.Fatalf("exit %d, output:\n%s", code, out)
	}
}

func TestModeRequired(t *testing.T) {
	_, errOut, code := runBinary(t, "", nil)
	if code != 2 || !strings.Contains(errOut, "--mode") {
		t.Fatalf("exit %d, stderr:\n%s", code, errOut)
	}
}

func TestRelayRefusesToStartOpen(t *testing.T) {
	key, _, err := cryptoutils.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"RELAY_ID=" + key, "RELAY_IP=127.0.0.1", "RELAY_TCP_PORT=" + freePort(t)}
	_, errOut, code := runBinary(t, "", env, "--mode", "relay")
	if code != 1 || !strings.Contains(errOut, "RELAY_OPEN") {
		t.Fatalf("exit %d, stderr:\n%s", code, errOut)
	}
}

func TestMissingConfig(t *testing.T) {
	_, errOut, code := runBinary(t, "", nil, "--mode", "peer")
	if code != 1 || !strings.Contains(errOut, "RELAY_IP") {
		t.Fatalf("exit %d, stderr:\n%s", code, errOut)
	}
}

func TestPeerCommands(t *testing.T) {
	// A real relay in this process, so the peer can bootstrap its DHT and join groups.
	relayKey, _, err := cryptoutils.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	t.Setenv("RELAY_ID", relayKey)
	t.Setenv("RELAY_IP", "127.0.0.1")
	t.Setenv("RELAY_TCP_PORT", port)
	t.Setenv("RELAY_OPEN", "true")
	t.Setenv("RELAY_DB", filepath.Join(t.TempDir(), "mailbox.db"))
	cfg, err := config.LoadConfig(config.ModeRelay)
	if err != nil {
		t.Fatal(err)
	}
	r, err := relay.Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	peerKey, _, err := cryptoutils.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	_, bobID, err := cryptoutils.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PEER_ID=" + peerKey, "RELAY_PEER_ID=" + r.Host.ID().String(),
		"RELAY_IP=127.0.0.1", "RELAY_TCP_PORT=" + port,
		"PEER_DB=" + filepath.Join(t.TempDir(), "peer.db"),
	}
	script := strings.Join([]string{
		"help",
		"bogus",
		"group newsecret",
		"group join mygroup short",
		"group list",
		"contact add bob " + bobID.String(),
		"contact list",
		"exit",
	}, "\n") + "\n"
	out, errOut, code := runBinary(t, script, env, "--mode", "peer")
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"Unknown command: bogus",
		"New group secret",
		"Joined #mygroup",
		"this secret is short",
		"Added contact bob",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}
