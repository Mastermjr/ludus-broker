package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultWireGuardDir = "/etc/wireguard"
	defaultStateDir     = "/var/lib/ludus/viberange-wireguard"
)

type commandRunner interface {
	Run(name string, input []byte, args ...string) ([]byte, error)
}

type osCommandRunner struct{}

func (osCommandRunner) Run(name string, input []byte, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.Stdin = bytes.NewReader(input)
	output, err := command.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s failed: %s", name, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("run %s: %w", name, err)
	}
	return output, nil
}

type peerState struct {
	UserID          string     `json:"userID"`
	Generation      int64      `json:"generation"`
	PeerID          string     `json:"peerID"`
	PublicKey       string     `json:"publicKey"`
	IssuedAt        time.Time  `json:"issuedAt"`
	RevokedAt       *time.Time `json:"revokedAt,omitempty"`
	ClientIP        string     `json:"clientIP"`
	AllowedRange    string     `json:"allowedRange"`
	LatestHandshake *time.Time `json:"latestHandshake,omitempty"`
}

type issuedProfile struct {
	UserID     string
	Generation int64
	PeerID     string
	IssuedAt   time.Time
	Profile    []byte
}

type rotator struct {
	wireGuardDir string
	stateDir     string
	endpoint     string
	port         int
	runner       commandRunner
	now          func() time.Time
	mu           sync.Mutex
}

func newRotator(endpoint string, port int) *rotator {
	return &rotator{
		wireGuardDir: defaultWireGuardDir,
		stateDir:     defaultStateDir,
		endpoint:     endpoint,
		port:         port,
		runner:       osCommandRunner{},
		now:          time.Now,
	}
}

func (r *rotator) issue(userID string, userNumber, rangeNumber int, generation int64) (issuedProfile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateTarget(userID, userNumber, rangeNumber, generation); err != nil {
		return issuedProfile{}, err
	}
	if r.endpoint == "" || r.port < 1 || r.port > 65535 {
		return issuedProfile{}, errors.New("WireGuard endpoint configuration is invalid")
	}
	previous, err := r.loadState(userID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return issuedProfile{}, err
	}
	if err == nil && generation < previous.Generation {
		return issuedProfile{}, fmt.Errorf("generation %d is older than current generation %d", generation, previous.Generation)
	}

	privateKey, err := r.runner.Run("wg", nil, "genkey")
	if err != nil {
		return issuedProfile{}, err
	}
	privateKey = bytes.TrimSpace(privateKey)
	defer zero(privateKey)
	if len(privateKey) == 0 {
		return issuedProfile{}, errors.New("wg genkey returned an empty key")
	}
	publicKey, err := r.runner.Run("wg", append(append([]byte(nil), privateKey...), '\n'), "pubkey")
	if err != nil {
		return issuedProfile{}, err
	}
	publicKey = bytes.TrimSpace(publicKey)
	if len(publicKey) == 0 {
		return issuedProfile{}, errors.New("wg pubkey returned an empty key")
	}
	serverPublicKey, err := os.ReadFile(filepath.Join(r.wireGuardDir, "server-public-key"))
	if err != nil {
		return issuedProfile{}, fmt.Errorf("read server public key: %w", err)
	}
	serverPublicKey = bytes.TrimSpace(serverPublicKey)
	clientIP := "198.51.100." + strconv.Itoa(userNumber) + "/32"
	allowedRange := "10." + strconv.Itoa(rangeNumber) + ".0.0/16"
	profile := []byte(fmt.Sprintf(`[Interface]
PrivateKey = %s
Address = %s

[Peer]
PublicKey = %s
Endpoint = %s:%d
AllowedIPs = %s
PersistentKeepalive = 25
`, privateKey, clientIP, serverPublicKey, r.endpoint, r.port, allowedRange))

	configPath := filepath.Join(r.wireGuardDir, "wg0.conf")
	oldConfig, err := os.ReadFile(configPath)
	if err != nil {
		zero(profile)
		return issuedProfile{}, fmt.Errorf("read wg0.conf: %w", err)
	}
	publicPath := filepath.Join(r.wireGuardDir, userID+"-client-public-key")
	oldPublic, publicReadErr := os.ReadFile(publicPath)
	publicExisted := publicReadErr == nil
	if publicReadErr != nil && !errors.Is(publicReadErr, os.ErrNotExist) {
		zero(profile)
		return issuedProfile{}, fmt.Errorf("read prior client public key: %w", publicReadErr)
	}
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		zero(profile)
		return issuedProfile{}, fmt.Errorf("create plugin state directory: %w", err)
	}
	issuedAt := r.now().UTC()
	fingerprint := sha256.Sum256(publicKey)
	state := peerState{
		UserID: userID, Generation: generation,
		PeerID:    "sha256:" + hex.EncodeToString(fingerprint[:]),
		PublicKey: string(publicKey), IssuedAt: issuedAt,
		ClientIP: clientIP, AllowedRange: allowedRange,
	}
	stateBytes, err := json.Marshal(state)
	if err != nil {
		zero(profile)
		return issuedProfile{}, err
	}
	// This public-only intent closes the crash window between syncconf and the
	// final state write: recovery can always identify and revoke the generation.
	if err := atomicWrite(r.pendingPath(userID), append(stateBytes, '\n'), 0o600); err != nil {
		zero(profile)
		return issuedProfile{}, fmt.Errorf("persist peer rotation intent: %w", err)
	}
	if err := atomicWrite(publicPath, append(publicKey, '\n'), 0o600); err != nil {
		_ = os.Remove(r.pendingPath(userID))
		zero(profile)
		return issuedProfile{}, fmt.Errorf("persist client public key: %w", err)
	}
	restorePublic := func() {
		if publicExisted {
			_ = atomicWrite(publicPath, oldPublic, 0o600)
		} else {
			_ = os.Remove(publicPath)
		}
	}
	rollback := func(removeIntent bool) {
		_ = atomicWrite(configPath, oldConfig, 0o600)
		_ = r.sync()
		restorePublic()
		if removeIntent {
			_ = os.Remove(r.pendingPath(userID))
		}
	}
	block := fmt.Sprintf("[Peer]\nPublicKey = %s\nAllowedIPs = 198.51.100.%d/32", publicKey, userNumber)
	newConfig, err := replaceManagedBlock(oldConfig, userID, block)
	if err != nil {
		rollback(true)
		zero(profile)
		return issuedProfile{}, err
	}
	if err := atomicWrite(configPath, newConfig, 0o600); err != nil {
		rollback(true)
		zero(profile)
		return issuedProfile{}, err
	}
	if err := r.sync(); err != nil {
		rollback(true)
		zero(profile)
		return issuedProfile{}, err
	}
	if err := atomicWrite(r.statePath(userID), append(stateBytes, '\n'), 0o600); err != nil {
		rollback(false)
		zero(profile)
		return issuedProfile{}, fmt.Errorf("persist non-secret peer state: %w", err)
	}
	_ = os.Remove(r.pendingPath(userID))
	// Ludus' stock endpoint reads this file. Removing it ensures the profile can
	// only be obtained from the one issue response and cannot later be fetched.
	_ = os.Remove(filepath.Join(r.wireGuardDir, userID+"-client-private-key"))
	return issuedProfile{UserID: userID, Generation: generation, PeerID: state.PeerID, IssuedAt: issuedAt, Profile: profile}, nil
}

func (r *rotator) revoke(userID string, generation int64) (peerState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !regexp.MustCompile(`^user[1-5]$`).MatchString(userID) || generation < 0 {
		return peerState{}, errors.New("allowed userID and non-negative generation are required")
	}
	state, err := r.loadState(userID)
	if err == nil && generation != state.Generation {
		pending, pendingErr := r.loadPending(userID)
		if pendingErr == nil && pending.Generation == generation {
			state = pending
		} else {
			return peerState{}, fmt.Errorf("generation %d does not match current generation %d", generation, state.Generation)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		pending, pendingErr := r.loadPending(userID)
		if pendingErr == nil && pending.Generation == generation {
			state = pending
		} else {
			state = peerState{UserID: userID, Generation: generation, PeerID: "legacy-untracked"}
		}
	} else if err != nil {
		return peerState{}, err
	}
	if state.RevokedAt != nil {
		return state, nil
	}
	configPath := filepath.Join(r.wireGuardDir, "wg0.conf")
	oldConfig, err := os.ReadFile(configPath)
	if err != nil {
		return peerState{}, fmt.Errorf("read wg0.conf: %w", err)
	}
	newConfig, err := removeManagedBlock(oldConfig, userID)
	if err != nil {
		return peerState{}, err
	}
	if err := atomicWrite(configPath, newConfig, 0o600); err != nil {
		return peerState{}, err
	}
	if err := r.sync(); err != nil {
		_ = atomicWrite(configPath, oldConfig, 0o600)
		_ = r.sync()
		return peerState{}, err
	}
	now := r.now().UTC()
	state.RevokedAt = &now
	stateBytes, err := json.Marshal(state)
	if err != nil {
		return peerState{}, err
	}
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		return peerState{}, fmt.Errorf("create plugin state directory: %w", err)
	}
	if err := atomicWrite(r.statePath(userID), append(stateBytes, '\n'), 0o600); err != nil {
		return peerState{}, err
	}
	_ = os.Remove(filepath.Join(r.wireGuardDir, userID+"-client-public-key"))
	_ = os.Remove(filepath.Join(r.wireGuardDir, userID+"-client-private-key"))
	_ = os.Remove(r.pendingPath(userID))
	return state, nil
}

func (r *rotator) status(userID string) (peerState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, err := r.loadState(userID)
	if err != nil || state.RevokedAt != nil || state.PublicKey == "" {
		return state, err
	}
	output, err := r.runner.Run("wg", nil, "show", "wg0", "latest-handshakes")
	if err != nil {
		return peerState{}, fmt.Errorf("read WireGuard handshake status: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != state.PublicKey {
			continue
		}
		unixSeconds, parseErr := strconv.ParseInt(fields[1], 10, 64)
		if parseErr != nil {
			return peerState{}, errors.New("WireGuard returned an invalid handshake timestamp")
		}
		if unixSeconds > 0 {
			value := time.Unix(unixSeconds, 0).UTC()
			state.LatestHandshake = &value
		}
		break
	}
	return state, nil
}

func (r *rotator) sync() error {
	stripped, err := r.runner.Run("wg-quick", nil, "strip", "wg0")
	if err != nil {
		return fmt.Errorf("render live WireGuard configuration: %w", err)
	}
	if _, err := r.runner.Run("wg", stripped, "syncconf", "wg0", "/dev/stdin"); err != nil {
		return fmt.Errorf("apply live WireGuard configuration: %w", err)
	}
	return nil
}

func (r *rotator) statePath(userID string) string {
	return filepath.Join(r.stateDir, userID+".json")
}

func (r *rotator) pendingPath(userID string) string {
	return filepath.Join(r.stateDir, userID+".pending.json")
}

func (r *rotator) loadPending(userID string) (peerState, error) {
	return loadPeerState(r.pendingPath(userID))
}

func (r *rotator) loadState(userID string) (peerState, error) {
	return loadPeerState(r.statePath(userID))
}

func loadPeerState(path string) (peerState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return peerState{}, err
	}
	var state peerState
	if err := json.Unmarshal(data, &state); err != nil {
		return peerState{}, fmt.Errorf("decode peer state: %w", err)
	}
	return state, nil
}

func validateTarget(userID string, userNumber, rangeNumber int, generation int64) error {
	if !regexp.MustCompile(`^user[1-5]$`).MatchString(userID) {
		return errors.New("target is not an allowed VibeRange participant user")
	}
	if userNumber < 2 || userNumber > 254 {
		return errors.New("Ludus user number is outside the WireGuard address range")
	}
	if rangeNumber < 2 || rangeNumber > 254 {
		return errors.New("Ludus range number is outside the participant route range")
	}
	if generation <= 0 {
		return errors.New("generation must be positive")
	}
	return nil
}

func replaceManagedBlock(config []byte, userID, block string) ([]byte, error) {
	without, err := removeManagedBlock(config, userID)
	if err != nil {
		return nil, err
	}
	begin := "# BEGIN ANSIBLE MANAGED BLOCK FOR " + userID
	end := "# END ANSIBLE MANAGED BLOCK FOR " + userID
	return []byte(strings.TrimRight(string(without), "\n") + "\n" + begin + "\n" + block + "\n" + end + "\n"), nil
}

func removeManagedBlock(config []byte, userID string) ([]byte, error) {
	if !regexp.MustCompile(`^user[1-5]$`).MatchString(userID) {
		return nil, errors.New("invalid managed-block user")
	}
	pattern := `(?ms)^# BEGIN ANSIBLE MANAGED BLOCK FOR ` + regexp.QuoteMeta(userID) + `\n.*?^# END ANSIBLE MANAGED BLOCK FOR ` + regexp.QuoteMeta(userID) + `\n?`
	re := regexp.MustCompile(pattern)
	matches := re.FindAllIndex(config, -1)
	if len(matches) > 1 {
		return nil, errors.New("wg0.conf contains duplicate managed blocks")
	}
	return re.ReplaceAll(config, nil), nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".viberange-*")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func zero(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
