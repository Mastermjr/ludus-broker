package ludus

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type HTTPClient struct {
	baseURL                     *url.URL
	apiKey                      string
	client                      *http.Client
	expectedBlueprintHash       []byte
	expectedBlueprintHashByUser map[string][]byte
	pollInterval                time.Duration
	operationTimeout            time.Duration
}

type HTTPOptions struct {
	BaseURL               string
	APIKeyFile            string
	Timeout               time.Duration
	InsecureSkipVerify    bool
	TLSCertSHA256         string
	BlueprintSHA256       string
	BlueprintSHA256ByUser map[string]string
	PollInterval          time.Duration
	OperationTimeout      time.Duration
}

func NewHTTPClient(options HTTPOptions) (*HTTPClient, error) {
	baseURL, err := url.Parse(strings.TrimRight(options.BaseURL, "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid Ludus URL")
	}
	if baseURL.Scheme != "https" {
		return nil, errors.New("Ludus URL must use HTTPS")
	}
	key, err := os.ReadFile(options.APIKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read Ludus API key: %w", err)
	}
	apiKey := strings.TrimSpace(string(key))
	if apiKey == "" {
		return nil, errors.New("Ludus API key file is empty")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	tlsConfig, err := ludusTLSConfig(options)
	if err != nil {
		return nil, err
	}
	var expectedBlueprintHash []byte
	if value := strings.TrimSpace(options.BlueprintSHA256); value != "" {
		expectedBlueprintHash, err = decodeSHA256(value)
		if err != nil {
			return nil, errors.New("VBR_LUDUS_BLUEPRINT_SHA256 must be a SHA-256 hash")
		}
	}
	expectedBlueprintHashByUser := make(map[string][]byte, len(options.BlueprintSHA256ByUser))
	for userID, value := range options.BlueprintSHA256ByUser {
		decoded, decodeErr := decodeSHA256(value)
		if decodeErr != nil {
			return nil, fmt.Errorf("blueprint hash for %s must be a SHA-256 hash", userID)
		}
		expectedBlueprintHashByUser[userID] = decoded
	}
	pollInterval := options.PollInterval
	if pollInterval <= 0 {
		pollInterval = 5 * time.Second
	}
	operationTimeout := options.OperationTimeout
	if operationTimeout <= 0 {
		operationTimeout = 15 * time.Minute
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &HTTPClient{
		baseURL:                     baseURL,
		apiKey:                      apiKey,
		client:                      &http.Client{Transport: transport, Timeout: timeout},
		expectedBlueprintHash:       expectedBlueprintHash,
		expectedBlueprintHashByUser: expectedBlueprintHashByUser,
		pollInterval:                pollInterval,
		operationTimeout:            operationTimeout,
	}, nil
}

func decodeSHA256(value string) ([]byte, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != sha256.Size {
		return nil, errors.New("invalid SHA-256 hash")
	}
	return decoded, nil
}

func ludusTLSConfig(options HTTPOptions) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: options.InsecureSkipVerify} // #nosec G402 -- explicit development option
	pin := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(options.TLSCertSHA256), ":", ""))
	if pin == "" {
		return config, nil
	}
	expected, err := hex.DecodeString(pin)
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("VBR_LUDUS_TLS_CERT_SHA256 must be a SHA-256 certificate fingerprint")
	}
	// The deployed Ludus certificate may be expired, but an exact certificate
	// fingerprint still authenticates the private management endpoint. Rotation
	// fails closed until the operator records the replacement fingerprint.
	config.InsecureSkipVerify = true // #nosec G402 -- replaced by exact leaf-certificate pin below
	config.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.PeerCertificates) == 0 {
			return errors.New("Ludus TLS peer did not present a certificate")
		}
		actual := sha256.Sum256(state.PeerCertificates[0].Raw)
		if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
			return errors.New("Ludus TLS certificate fingerprint mismatch")
		}
		return nil
	}
	return config, nil
}

func (c *HTTPClient) Version(ctx context.Context) (Version, error) {
	var response struct {
		Version string `json:"version"`
	}
	if err := c.get(ctx, "/api/v2/", nil, &response); err != nil {
		return Version{}, err
	}
	if response.Version == "" {
		return Version{}, errors.New("Ludus version response omitted version")
	}
	return Version{Server: response.Version, API: "v2"}, nil
}

func (c *HTTPClient) Inventory(ctx context.Context, userID string) (Range, error) {
	if userID == "" {
		return Range{}, errors.New("Ludus user ID is required")
	}
	query := url.Values{"userID": []string{userID}}
	var response struct {
		ID      string `json:"rangeID"`
		State   string `json:"rangeState"`
		Testing bool   `json:"testingEnabled"`
		VMs     []struct {
			Name      string `json:"name"`
			IP        string `json:"ip"`
			PoweredOn bool   `json:"poweredOn"`
		} `json:"VMs"`
	}
	if err := c.get(ctx, "/api/v2/range", query, &response); err != nil {
		return Range{}, err
	}
	if response.ID == "" {
		return Range{}, errors.New("Ludus range response omitted rangeID")
	}
	result := Range{ID: response.ID, State: response.State, Testing: response.Testing}
	for _, vm := range response.VMs {
		result.VMs = append(result.VMs, VM{Name: vm.Name, IP: vm.IP, PoweredOn: vm.PoweredOn})
	}
	return result, nil
}

func (c *HTTPClient) WireGuardHealth(ctx context.Context) (WireGuardCapability, error) {
	var response WireGuardCapability
	if err := c.get(ctx, "/api/v2/user/viberange-wireguard/health", nil, &response); err != nil {
		return WireGuardCapability{}, err
	}
	if !response.Ready || response.Name != "viberange-wireguard" || response.Version == "" {
		return WireGuardCapability{}, errors.New("Ludus WireGuard plugin health response was incomplete")
	}
	return response, nil
}

func (c *HTTPClient) IssueWireGuard(ctx context.Context, userID string, generation int64) (*WireGuardAccess, error) {
	if userID == "" || generation <= 0 {
		return nil, errors.New("Ludus user ID and positive generation are required")
	}
	var response struct {
		Result struct {
			WireGuardPeer
			Profile string `json:"profile"`
		} `json:"result"`
	}
	body, err := c.post(ctx, "/api/v2/user/viberange-wireguard/issue", map[string]any{"userID": userID, "generation": generation})
	if err != nil {
		return nil, err
	}
	defer zeroBytes(body)
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode Ludus WireGuard issue response: %w", err)
	}
	if response.Result.UserID != userID || response.Result.Generation != generation || response.Result.PeerID == "" || response.Result.Profile == "" {
		response.Result.Profile = ""
		return nil, errors.New("Ludus WireGuard issue response was incomplete or mismatched")
	}
	profile := []byte(response.Result.Profile)
	response.Result.Profile = ""
	access := NewWireGuardAccess(response.Result.WireGuardPeer, profile)
	zeroBytes(profile)
	return access, nil
}

func (c *HTTPClient) RevokeWireGuard(ctx context.Context, userID string, generation int64) (WireGuardPeer, error) {
	if userID == "" || generation < 0 {
		return WireGuardPeer{}, errors.New("Ludus user ID and non-negative generation are required")
	}
	var response struct {
		Result WireGuardPeer `json:"result"`
	}
	body, err := c.post(ctx, "/api/v2/user/viberange-wireguard/revoke", map[string]any{"userID": userID, "generation": generation})
	if err != nil {
		return WireGuardPeer{}, err
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return WireGuardPeer{}, fmt.Errorf("decode Ludus WireGuard revoke response: %w", err)
	}
	if response.Result.UserID != userID || response.Result.Generation != generation || !response.Result.Revoked {
		return WireGuardPeer{}, errors.New("Ludus WireGuard revoke response was incomplete or mismatched")
	}
	return response.Result, nil
}

func (c *HTTPClient) WireGuardStatus(ctx context.Context, userID string) (WireGuardPeer, error) {
	if userID == "" {
		return WireGuardPeer{}, errors.New("Ludus user ID is required")
	}
	query := url.Values{"userID": []string{userID}}
	var response struct {
		Result WireGuardPeer `json:"result"`
	}
	if err := c.get(ctx, "/api/v2/user/viberange-wireguard/status", query, &response); err != nil {
		return WireGuardPeer{}, err
	}
	if response.Result.UserID != userID || response.Result.Generation <= 0 || response.Result.PeerID == "" {
		return WireGuardPeer{}, errors.New("Ludus WireGuard status response was incomplete or mismatched")
	}
	return response.Result, nil
}

func (c *HTTPClient) ResetRange(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("Ludus user ID is required")
	}
	if _, err := c.ValidateBlueprint(ctx, userID); err != nil {
		return err
	}
	inventory, err := c.Inventory(ctx, userID)
	if err != nil {
		return fmt.Errorf("load range before reset: %w", err)
	}
	if !inventory.Testing {
		return errors.New("refusing reset because the range is not in Ludus testing state and has no active testing baseline")
	}
	if _, err := c.post(ctx, "/api/v2/user/viberange-wireguard/cleanup", map[string]any{"userID": userID}); err != nil {
		return fmt.Errorf("clean router capture state before reset: %w", err)
	}
	query := url.Values{"userID": []string{userID}}
	if err := c.put(ctx, "/api/v2/testing/stop", query, map[string]any{"force": false}); err != nil {
		return fmt.Errorf("revert testing baseline: %w", err)
	}
	if err := c.waitFor(ctx, "testing state to stop", func() (bool, string, error) {
		state, checkErr := c.Inventory(ctx, userID)
		if checkErr != nil {
			return false, "", checkErr
		}
		return !state.Testing, fmt.Sprintf("state=%s testing=%v", state.State, state.Testing), nil
	}); err != nil {
		return err
	}
	if err := c.put(ctx, "/api/v2/range/poweron", query, map[string]any{"machines": []string{"all"}}); err != nil {
		return fmt.Errorf("power on reset range: %w", err)
	}
	if err := c.waitFor(ctx, "reverted range readiness", func() (bool, string, error) {
		readiness, checkErr := c.VerifyRange(ctx, userID)
		if checkErr != nil {
			return false, "", checkErr
		}
		return readiness.Ready, strings.Join(readiness.Failures, "; "), nil
	}); err != nil {
		return err
	}
	if err := c.put(ctx, "/api/v2/testing/start", query, nil); err != nil {
		return fmt.Errorf("create clean testing baseline: %w", err)
	}
	if err := c.waitFor(ctx, "recreated testing baseline readiness", func() (bool, string, error) {
		state, checkErr := c.Inventory(ctx, userID)
		if checkErr != nil {
			return false, "", checkErr
		}
		if !state.Testing {
			return false, "testing=false", nil
		}
		readiness, checkErr := c.VerifyRange(ctx, userID)
		if checkErr != nil {
			return false, "", checkErr
		}
		return readiness.Ready, strings.Join(readiness.Failures, "; "), nil
	}); err != nil {
		return err
	}
	return nil
}

func (c *HTTPClient) waitFor(ctx context.Context, description string, check func() (bool, string, error)) error {
	deadline := time.NewTimer(c.operationTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	last := "condition not met"
	for {
		ready, detail, err := check()
		if err != nil {
			return fmt.Errorf("check %s: %w", description, err)
		}
		if ready {
			return nil
		}
		if detail != "" {
			last = detail
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %s: %s", description, last)
		case <-ticker.C:
		}
	}
}

func (c *HTTPClient) ValidateBlueprint(ctx context.Context, userID string) (string, error) {
	if userID == "" {
		return "", errors.New("Ludus user ID is required")
	}
	expected := c.expectedBlueprintHashByUser[userID]
	if len(expected) != sha256.Size {
		expected = c.expectedBlueprintHash
	}
	if len(expected) != sha256.Size {
		return "", fmt.Errorf("refusing lifecycle mutation because blueprint hash is not configured for %s", userID)
	}
	query := url.Values{"userID": []string{userID}}
	var response struct {
		Result string `json:"result"`
	}
	if err := c.get(ctx, "/api/v2/range/config", query, &response); err != nil {
		return "", fmt.Errorf("load range configuration: %w", err)
	}
	if response.Result == "" {
		return "", errors.New("Ludus range configuration response was empty")
	}
	actual := sha256.Sum256([]byte(response.Result))
	actualHex := hex.EncodeToString(actual[:])
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return actualHex, fmt.Errorf("refusing lifecycle mutation because range %s differs from canonical blueprint (actual SHA-256 %s)", userID, actualHex)
	}
	return actualHex, nil
}

func (c *HTTPClient) VerifyRange(ctx context.Context, userID string) (Readiness, error) {
	rangeState, err := c.Inventory(ctx, userID)
	if err != nil {
		return Readiness{}, err
	}
	result := Readiness{Ready: rangeState.State == "SUCCESS", CheckedAt: time.Now().UTC()}
	if rangeState.State != "SUCCESS" {
		result.Failures = append(result.Failures, "range state is "+rangeState.State)
	}
	for _, vm := range rangeState.VMs {
		if !vm.PoweredOn {
			result.Ready = false
			result.Failures = append(result.Failures, vm.Name+" is powered off")
		}
	}
	if len(rangeState.VMs) != 4 {
		result.Ready = false
		result.Failures = append(result.Failures, fmt.Sprintf("range has %d VMs; expected 4", len(rangeState.VMs)))
	}
	return result, nil
}

func (c *HTTPClient) get(ctx context.Context, path string, query url.Values, destination any) error {
	body, err := c.request(ctx, http.MethodGet, path, query, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, destination); err != nil {
		return fmt.Errorf("decode Ludus response: %w", err)
	}
	return nil
}

func (c *HTTPClient) post(ctx context.Context, path string, payload any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode Ludus request: %w", err)
	}
	defer zeroBytes(body)
	return c.request(ctx, http.MethodPost, path, nil, body)
}

func (c *HTTPClient) put(ctx context.Context, path string, query url.Values, payload any) error {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode Ludus request: %w", err)
		}
	}
	_, err = c.request(ctx, http.MethodPut, path, query, body)
	return err
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (c *HTTPClient) request(ctx context.Context, method, path string, query url.Values, payload []byte) ([]byte, error) {
	endpoint := *c.baseURL
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + path
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-API-KEY", c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ludus-viberange-broker")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("Ludus request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read Ludus response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &apiError)
		if apiError.Error == "" {
			apiError.Error = http.StatusText(response.StatusCode)
		}
		return nil, fmt.Errorf("Ludus returned HTTP %d: %s", response.StatusCode, apiError.Error)
	}
	return body, nil
}
