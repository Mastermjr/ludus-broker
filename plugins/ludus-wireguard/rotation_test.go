package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	failSync    bool
	calls       []string
	qmList      string
	guestResult string
}

func (f *fakeRunner) Run(name string, input []byte, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, call)
	if call == "qm list" {
		return []byte(f.qmList), nil
	}
	if strings.HasPrefix(call, "qm guest exec ") {
		return []byte(f.guestResult), nil
	}
	switch call {
	case "wg genkey":
		return []byte("new-private\n"), nil
	case "wg pubkey":
		if string(input) != "new-private\n" {
			return nil, errors.New("wrong private key input")
		}
		return []byte("new-public\n"), nil
	case "wg-quick strip wg0":
		return []byte("stripped"), nil
	case "wg show wg0 latest-handshakes":
		return []byte("new-public\t1794186000\n"), nil
	case "wg syncconf wg0 /dev/stdin":
		if f.failSync {
			return nil, errors.New("sync failed")
		}
		if !bytes.Equal(input, []byte("stripped")) {
			return nil, errors.New("wrong sync input")
		}
		return nil, nil
	default:
		return nil, errors.New("unexpected command: " + call)
	}
}

func testRotator(t *testing.T, runner *fakeRunner) *rotator {
	t.Helper()
	wgDir := filepath.Join(t.TempDir(), "wireguard")
	stateDir := filepath.Join(t.TempDir(), "state")
	if err := os.MkdirAll(wgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[Interface]\nPrivateKey = server\n# BEGIN ANSIBLE MANAGED BLOCK FOR user1\n[Peer]\nPublicKey = old-public\nAllowedIPs = 198.51.100.6/32\n# END ANSIBLE MANAGED BLOCK FOR user1\n"
	if err := os.WriteFile(filepath.Join(wgDir, "wg0.conf"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wgDir, "server-public-key"), []byte("server-public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &rotator{wireGuardDir: wgDir, stateDir: stateDir, endpoint: "vpn.example", port: 51820, runner: runner, now: func() time.Time { return time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC) }}
}

func TestIssueReplacesPeerAndReturnsProfileWithoutPersistingPrivateKey(t *testing.T) {
	runner := &fakeRunner{}
	value := testRotator(t, runner)
	issued, err := value.issue("user1", 6, 8, 4)
	if err != nil {
		t.Fatal(err)
	}
	profile := string(issued.Profile)
	if !strings.Contains(profile, "PrivateKey = new-private") || !strings.Contains(profile, "Address = 198.51.100.6/32") || !strings.Contains(profile, "AllowedIPs = 10.8.0.0/16") {
		t.Fatalf("unexpected profile: %s", profile)
	}
	config, err := os.ReadFile(filepath.Join(value.wireGuardDir, "wg0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "old-public") || !strings.Contains(string(config), "PublicKey = new-public") {
		t.Fatalf("unexpected config: %s", config)
	}
	persisted, err := os.ReadFile(value.statePath("user1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte("new-private")) || bytes.Contains(persisted, []byte("[Interface]")) {
		t.Fatalf("secret persisted in state: %s", persisted)
	}
	if _, err := os.Stat(filepath.Join(value.wireGuardDir, "user1-client-private-key")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private key file exists: %v", err)
	}
	status, err := value.status("user1")
	if err != nil || status.LatestHandshake == nil {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}

func TestRevokeIsGenerationBoundAndIdempotent(t *testing.T) {
	value := testRotator(t, &fakeRunner{})
	issued, err := value.issue("user1", 6, 6, 7)
	if err != nil {
		t.Fatal(err)
	}
	zero(issued.Profile)
	if _, err := value.revoke("user1", 6); err == nil {
		t.Fatal("stale generation revoked current peer")
	}
	state, err := value.revoke("user1", 7)
	if err != nil || state.RevokedAt == nil {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := value.revoke("user1", 7); err != nil {
		t.Fatalf("idempotent revoke failed: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(value.wireGuardDir, "wg0.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "ANSIBLE MANAGED BLOCK FOR user1") {
		t.Fatalf("peer remained after revoke: %s", config)
	}
}

func TestRevokeUsesPendingIntentAfterInterruptedIssue(t *testing.T) {
	value := testRotator(t, &fakeRunner{})
	if err := os.MkdirAll(value.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(value.statePath("user1"), []byte(`{"userID":"user1","generation":6,"peerID":"old","publicKey":"old-public"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := atomicWrite(value.pendingPath("user1"), []byte(`{"userID":"user1","generation":7,"peerID":"new","publicKey":"new-public"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := value.revoke("user1", 7)
	if err != nil || state.RevokedAt == nil || state.Generation != 7 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if _, err := os.Stat(value.pendingPath("user1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending intent remained: %v", err)
	}
}

func TestIssueRestoresConfigurationWhenLiveSyncFails(t *testing.T) {
	runner := &fakeRunner{failSync: true}
	value := testRotator(t, runner)
	original, _ := os.ReadFile(filepath.Join(value.wireGuardDir, "wg0.conf"))
	if _, err := value.issue("user1", 6, 6, 3); err == nil {
		t.Fatal("issue succeeded despite sync failure")
	}
	after, _ := os.ReadFile(filepath.Join(value.wireGuardDir, "wg0.conf"))
	if !bytes.Equal(original, after) {
		t.Fatalf("configuration was not restored\nwant: %s\ngot: %s", original, after)
	}
}

func TestCleanupRouterStopsCaptureAndClearsTransientState(t *testing.T) {
	runner := &fakeRunner{
		qmList:      " VMID NAME STATUS\n 110 user1-router-debian12-x64 running\n 113 user1-clickhouse-ir running\n",
		guestResult: `{"exitcode":0,"exited":1,"out-data":""}`,
	}
	value := testRotator(t, runner)
	if err := value.cleanupRouter("user1"); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 || runner.calls[0] != "qm list" || !strings.HasPrefix(runner.calls[1], "qm guest exec 110 -- /bin/bash -lc ") {
		t.Fatalf("calls=%q", runner.calls)
	}
	for _, required := range []string{"viberange-pcap@*.service", "/var/lib/viberange-router-evidence/runs/*", "/var/lib/vector/*", "systemctl start vector.service"} {
		if !strings.Contains(runner.calls[1], required) {
			t.Errorf("cleanup command omitted %q", required)
		}
	}
}

func TestCleanupRouterFailsClosed(t *testing.T) {
	for name, runner := range map[string]*fakeRunner{
		"missing router": {qmList: "110 other-router running\n"},
		"guest failure":  {qmList: "110 user1-router-debian12-x64 running\n", guestResult: `{"exitcode":1,"exited":1,"err-data":"failed"}`},
	} {
		t.Run(name, func(t *testing.T) {
			if err := testRotator(t, runner).cleanupRouter("user1"); err == nil {
				t.Fatal("cleanup unexpectedly succeeded")
			}
		})
	}
	if err := testRotator(t, &fakeRunner{}).cleanupRouter("../../bad"); err == nil {
		t.Fatal("unsafe user ID accepted")
	}
}

func TestRejectsUnexpectedTargetsAndDuplicateBlocks(t *testing.T) {
	if err := validateTarget("root", 1, 1, 1); err == nil {
		t.Fatal("unexpected target accepted")
	}
	config := []byte("# BEGIN ANSIBLE MANAGED BLOCK FOR user1\nx\n# END ANSIBLE MANAGED BLOCK FOR user1\n# BEGIN ANSIBLE MANAGED BLOCK FOR user1\ny\n# END ANSIBLE MANAGED BLOCK FOR user1\n")
	if _, err := removeManagedBlock(config, "user1"); err == nil {
		t.Fatal("duplicate blocks accepted")
	}
}
