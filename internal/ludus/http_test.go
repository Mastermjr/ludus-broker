package ludus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPClientContracts(t *testing.T) {
	const adminKey = "admin-secret"
	const wireGuardProfile = "[Interface]\nPrivateKey = participant-secret\nAddress = 198.51.100.8/32\n"
	const canonicalConfig = "ludus:\n  - vm_name: canonical\n"
	testingEnabled := true
	var resetCalls []string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/", func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-KEY") != adminKey {
			t.Error("missing administrator API key")
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"version": "2.3.2"})
	})
	mux.HandleFunc("/api/v2/range", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("userID") != "user3" {
			t.Error("missing userID query")
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"rangeID": "user3", "rangeState": "SUCCESS", "testingEnabled": testingEnabled,
			"VMs": []map[string]any{
				{"name": "router", "ip": "10.8.10.254", "poweredOn": true},
				{"name": "analysis", "ip": "10.8.10.10", "poweredOn": true},
				{"name": "attacker", "ip": "10.8.20.20", "poweredOn": true},
				{"name": "victim", "ip": "10.8.30.30", "poweredOn": true},
			},
		})
	})
	mux.HandleFunc("/api/v2/range/config", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("userID") != "user3" {
			t.Error("missing config userID query")
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": canonicalConfig})
	})
	mux.HandleFunc("/api/v2/user/viberange-wireguard/health", func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"name": "viberange-wireguard", "version": "1.0.0", "ready": true})
	})
	mux.HandleFunc("/api/v2/user/viberange-wireguard/issue", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("issue method = %s", request.Method)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": map[string]any{
			"userID": "user3", "generation": 2, "peerID": "sha256:peer", "issuedAt": "2026-11-09T00:00:00Z", "profile": wireGuardProfile,
		}})
	})
	mux.HandleFunc("/api/v2/user/viberange-wireguard/revoke", func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": map[string]any{
			"userID": "user3", "generation": 2, "peerID": "sha256:peer", "revoked": true, "revokedAt": "2026-11-09T01:00:00Z",
		}})
	})
	mux.HandleFunc("/api/v2/user/viberange-wireguard/cleanup", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("cleanup method = %s", request.Method)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body["userID"] != "user3" {
			t.Errorf("cleanup body=%v err=%v", body, err)
		}
		resetCalls = append(resetCalls, "cleanup")
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": map[string]any{"userID": "user3", "clean": true}})
	})
	mux.HandleFunc("/api/v2/testing/stop", func(writer http.ResponseWriter, request *http.Request) {
		assertLudusMutation(t, request, "user3")
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"force":false}` {
			t.Errorf("testing stop body = %s", body)
		}
		resetCalls = append(resetCalls, "stop")
		testingEnabled = false
		writer.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/v2/range/poweron", func(writer http.ResponseWriter, request *http.Request) {
		assertLudusMutation(t, request, "user3")
		resetCalls = append(resetCalls, "poweron")
		writer.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/api/v2/testing/start", func(writer http.ResponseWriter, request *http.Request) {
		assertLudusMutation(t, request, "user3")
		resetCalls = append(resetCalls, "start")
		testingEnabled = true
		writer.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte(adminKey), 0o600); err != nil {
		t.Fatal(err)
	}
	blueprintHash := sha256.Sum256([]byte(canonicalConfig))
	client, err := NewHTTPClient(HTTPOptions{
		BaseURL: server.URL, APIKeyFile: keyFile, InsecureSkipVerify: true,
		BlueprintSHA256: hex.EncodeToString(blueprintHash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	version, err := client.Version(ctx)
	if err != nil || version.Server != "2.3.2" {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	inventory, err := client.Inventory(ctx, "user3")
	if err != nil || inventory.ID != "user3" || len(inventory.VMs) != 4 {
		t.Fatalf("inventory=%+v err=%v", inventory, err)
	}
	readiness, err := client.VerifyRange(ctx, "user3")
	if err != nil || !readiness.Ready {
		t.Fatalf("readiness=%+v err=%v", readiness, err)
	}
	capability, err := client.WireGuardHealth(ctx)
	if err != nil || !capability.Ready || capability.Name != "viberange-wireguard" {
		t.Fatalf("capability=%+v err=%v", capability, err)
	}
	access, err := client.IssueWireGuard(ctx, "user3", 2)
	if err != nil || access.Peer.PeerID != "sha256:peer" {
		t.Fatalf("access=%+v err=%v", access, err)
	}
	profile, err := access.ConsumeProfile()
	if err != nil || string(profile) != wireGuardProfile {
		t.Fatalf("profile returned=%v err=%v", len(profile) > 0, err)
	}
	zeroBytes(profile)
	peer, err := client.RevokeWireGuard(ctx, "user3", 2)
	if err != nil || !peer.Revoked {
		t.Fatalf("peer=%+v err=%v", peer, err)
	}
	if err := client.ResetRange(ctx, "user3"); err != nil {
		t.Fatalf("ResetRange() error = %v", err)
	}
	if got := strings.Join(resetCalls, ","); got != "cleanup,stop,poweron,start" {
		t.Fatalf("reset call order = %q", got)
	}
}

func TestValidateBlueprintUsesPerUserHash(t *testing.T) {
	const config = "slot-specific-firewall-config"
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/range/config", func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": config})
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("admin"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(config))
	client, err := NewHTTPClient(HTTPOptions{
		BaseURL: server.URL, APIKeyFile: keyFile, InsecureSkipVerify: true,
		BlueprintSHA256ByUser: map[string]string{"user5": hex.EncodeToString(hash[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ValidateBlueprint(context.Background(), "user5"); err != nil {
		t.Fatalf("per-user validation failed: %v", err)
	}
	if _, err := client.ValidateBlueprint(context.Background(), "user4"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing per-user hash error = %v", err)
	}
}

func TestResetRangeRejectsBlueprintDriftBeforeMutation(t *testing.T) {
	mutations := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/range/config", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": "drifted"})
	})
	mux.HandleFunc("/api/v2/testing/stop", func(writer http.ResponseWriter, _ *http.Request) {
		mutations++
		writer.WriteHeader(http.StatusCreated)
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("admin"), 0o600); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256([]byte("canonical"))
	client, err := NewHTTPClient(HTTPOptions{
		BaseURL: server.URL, APIKeyFile: keyFile, InsecureSkipVerify: true,
		BlueprintSHA256: hex.EncodeToString(expected[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.ResetRange(context.Background(), "user5")
	if err == nil || !strings.Contains(err.Error(), "differs from canonical blueprint") {
		t.Fatalf("ResetRange() error = %v", err)
	}
	if mutations != 0 {
		t.Fatalf("mutating calls = %d, want 0", mutations)
	}
}

func assertLudusMutation(t *testing.T, request *http.Request, userID string) {
	t.Helper()
	if request.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", request.Method)
	}
	if request.URL.Query().Get("userID") != userID {
		t.Errorf("userID = %q, want %q", request.URL.Query().Get("userID"), userID)
	}
}

func TestHTTPClientCertificatePin(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"version": "2.3.2"})
	}))
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("admin"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint := sha256.Sum256(server.Certificate().Raw)
	client, err := NewHTTPClient(HTTPOptions{
		BaseURL: server.URL, APIKeyFile: keyFile, TLSCertSHA256: hex.EncodeToString(fingerprint[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Version(context.Background()); err != nil {
		t.Fatalf("pinned Version() error = %v", err)
	}
	wrong := make([]byte, sha256.Size)
	client, err = NewHTTPClient(HTTPOptions{
		BaseURL: server.URL, APIKeyFile: keyFile, TLSCertSHA256: hex.EncodeToString(wrong),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Version(context.Background()); err == nil || !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("wrong certificate pin error = %v", err)
	}
}

func TestHTTPClientRejectsAmbiguousResponses(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/range", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"rangeState": "SUCCESS"})
	})
	mux.HandleFunc("/api/v2/user/viberange-wireguard/issue", func(writer http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(writer).Encode(map[string]any{"result": map[string]any{
			"userID": "other", "generation": 1, "peerID": "peer", "profile": "secret",
		}})
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyFile, []byte("admin"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewHTTPClient(HTTPOptions{BaseURL: server.URL, APIKeyFile: keyFile, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Inventory(context.Background(), "user3"); err == nil {
		t.Fatal("ambiguous range response accepted")
	}
	if _, err := client.IssueWireGuard(context.Background(), "user3", 1); err == nil {
		t.Fatal("wrong-user WireGuard profile accepted")
	}
}
