//go:build ludus_plugin

package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"sync"

	"github.com/pocketbase/pocketbase/core"
	"ludusapi"
)

const (
	routeBase     = "/api/v2/user/viberange-wireguard"
	pluginVersion = "1.1.0"
)

type wireGuardPlugin struct {
	mu               sync.RWMutex
	initialized      bool
	routesRegistered bool
	rotator          *rotator
}

// Plugin is the symbol loaded by Ludus' native Go plugin loader.
var Plugin wireGuardPlugin

func (p *wireGuardPlugin) Name() string { return "VibeRange WireGuard Rotation" }

func (p *wireGuardPlugin) Initialize(_ *ludusapi.Server) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if os.Geteuid() != 0 {
		return errors.New("VibeRange WireGuard plugin must run in the Ludus admin service")
	}
	p.rotator = newRotator(ludusapi.ServerConfiguration.ProxmoxPublicIP, ludusapi.ServerConfiguration.WireguardPort)
	p.initialized = true
	return nil
}

func (p *wireGuardPlugin) RegisterRoutes(app *core.App) {
	(*app).OnServe().BindFunc(func(event *core.ServeEvent) error {
		event.Router.GET(routeBase+"/health", p.health)
		event.Router.POST(routeBase+"/issue", p.issue)
		event.Router.POST(routeBase+"/revoke", p.revoke)
		event.Router.POST(routeBase+"/cleanup", p.cleanup)
		event.Router.GET(routeBase+"/status", p.status)
		return event.Next()
	})
	p.mu.Lock()
	p.routesRegistered = true
	p.mu.Unlock()
}

func (p *wireGuardPlugin) GetEmbeddedFSs() []fs.FS { return nil }
func (p *wireGuardPlugin) Shutdown() error         { return nil }

func (p *wireGuardPlugin) Initialized() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.initialized
}

func (p *wireGuardPlugin) RoutesRegistered() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.routesRegistered
}

func (p *wireGuardPlugin) health(event *core.RequestEvent) error {
	if err := authorize(event); err != nil {
		return event.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	}
	return event.JSON(http.StatusOK, map[string]any{"name": "viberange-wireguard", "version": pluginVersion, "ready": p.rotator != nil})
}

type mutationRequest struct {
	UserID     string `json:"userID"`
	Generation int64  `json:"generation"`
}

func (p *wireGuardPlugin) issue(event *core.RequestEvent) error {
	if err := authorize(event); err != nil {
		return event.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	}
	var request mutationRequest
	if err := event.BindBody(&request); err != nil {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if !allowedUser(request.UserID) || request.Generation <= 0 {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "allowed userID and positive generation are required"})
	}
	record, err := event.App.FindFirstRecordByData("users", "userID", request.UserID)
	if err != nil {
		return event.JSON(http.StatusNotFound, map[string]string{"error": "Ludus user not found"})
	}
	rangeID := record.GetString("defaultRangeID")
	if rangeID != request.UserID {
		return event.JSON(http.StatusConflict, map[string]string{"error": "participant default range does not match the requested slot"})
	}
	rangeRecord, err := event.App.FindFirstRecordByData("ranges", "rangeID", rangeID)
	if err != nil {
		return event.JSON(http.StatusNotFound, map[string]string{"error": "Ludus range not found"})
	}
	result, err := p.rotator.issue(request.UserID, record.GetInt("userNumber"), rangeRecord.GetInt("rangeNumber"), request.Generation)
	if err != nil {
		return event.JSON(http.StatusInternalServerError, map[string]string{"error": "WireGuard issue operation failed"})
	}
	defer zero(result.Profile)
	event.Response.Header().Set("Cache-Control", "no-store")
	event.Response.Header().Set("Pragma", "no-cache")
	return event.JSON(http.StatusCreated, map[string]any{"result": map[string]any{
		"userID": result.UserID, "generation": result.Generation, "peerID": result.PeerID,
		"issuedAt": result.IssuedAt, "profile": string(result.Profile),
	}})
}

func (p *wireGuardPlugin) revoke(event *core.RequestEvent) error {
	if err := authorize(event); err != nil {
		return event.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	}
	var request mutationRequest
	if err := event.BindBody(&request); err != nil {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if !allowedUser(request.UserID) || request.Generation < 0 {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "allowed userID and non-negative generation are required"})
	}
	state, err := p.rotator.revoke(request.UserID, request.Generation)
	if err != nil {
		return event.JSON(http.StatusConflict, map[string]string{"error": "WireGuard revoke operation failed"})
	}
	event.Response.Header().Set("Cache-Control", "no-store")
	return event.JSON(http.StatusOK, map[string]any{"result": map[string]any{
		"userID": state.UserID, "generation": state.Generation, "peerID": state.PeerID,
		"revoked": state.RevokedAt != nil, "revokedAt": state.RevokedAt,
	}})
}

func (p *wireGuardPlugin) cleanup(event *core.RequestEvent) error {
	if err := authorize(event); err != nil {
		return event.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	}
	var request mutationRequest
	if err := event.BindBody(&request); err != nil {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "invalid request body"})
	}
	if !allowedUser(request.UserID) {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "allowed userID is required"})
	}
	if err := p.rotator.cleanupRouter(request.UserID); err != nil {
		return event.JSON(http.StatusInternalServerError, map[string]string{"error": "router cleanup failed"})
	}
	return event.JSON(http.StatusOK, map[string]any{"result": map[string]any{"userID": request.UserID, "clean": true}})
}

func (p *wireGuardPlugin) status(event *core.RequestEvent) error {
	if err := authorize(event); err != nil {
		return event.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	}
	userID := event.Request.URL.Query().Get("userID")
	if !allowedUser(userID) {
		return event.JSON(http.StatusBadRequest, map[string]string{"error": "allowed userID is required"})
	}
	state, err := p.rotator.status(userID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return event.JSON(http.StatusNotFound, map[string]string{"error": "WireGuard peer state not found"})
		}
		return event.JSON(http.StatusInternalServerError, map[string]string{"error": "WireGuard status operation failed"})
	}
	event.Response.Header().Set("Cache-Control", "no-store")
	return event.JSON(http.StatusOK, map[string]any{"result": map[string]any{
		"userID": state.UserID, "generation": state.Generation, "peerID": state.PeerID,
		"issuedAt": state.IssuedAt, "revoked": state.RevokedAt != nil, "revokedAt": state.RevokedAt,
		"clientIP": state.ClientIP, "allowedRange": state.AllowedRange, "latestHandshake": state.LatestHandshake,
	}})
}

func authorize(event *core.RequestEvent) error {
	if event.Auth == nil {
		return errors.New("authentication required")
	}
	if event.Auth.GetString("userID") != "ROOT" && !event.Auth.GetBool("isAdmin") {
		return errors.New("administrator credential required")
	}
	return nil
}

func allowedUser(userID string) bool {
	return regexp.MustCompile(`^user[1-5]$`).MatchString(userID)
}
