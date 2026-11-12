package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const routerCleanupScript = `set -euo pipefail
systemctl stop 'viberange-pcap@*.service' 2>/dev/null || true
systemctl stop vector.service 2>/dev/null || true
rm -rf /var/lib/viberange-router-evidence/runs/*
rm -rf /run/viberange-router-evidence/*
find /var/log/viberange-router -mindepth 1 -maxdepth 1 -type f -delete 2>/dev/null || true
find /var/log/zeek/current -mindepth 1 -maxdepth 1 -type f -delete 2>/dev/null || true
rm -rf /var/lib/vector/*
systemctl start vector.service
`

type guestExecResult struct {
	ExitCode int    `json:"exitcode"`
	Exited   int    `json:"exited"`
	Error    string `json:"err-data"`
}

func (r *rotator) cleanupRouter(userID string) error {
	if !regexp.MustCompile(`^user[1-5]$`).MatchString(userID) {
		return errors.New("allowed userID is required")
	}
	output, err := r.runner.Run("qm", nil, "list")
	if err != nil {
		return fmt.Errorf("list Proxmox VMs: %w", err)
	}
	targetName := userID + "-router-debian12-x64"
	vmid := 0
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != targetName {
			continue
		}
		value, parseErr := strconv.Atoi(fields[0])
		if parseErr != nil || value <= 0 || vmid != 0 {
			return errors.New("router VM inventory was ambiguous or invalid")
		}
		vmid = value
	}
	if vmid == 0 {
		return errors.New("router VM was not found")
	}
	output, err = r.runner.Run("qm", nil, "guest", "exec", strconv.Itoa(vmid), "--", "/bin/bash", "-lc", routerCleanupScript)
	if err != nil {
		return fmt.Errorf("execute router cleanup through guest agent: %w", err)
	}
	var result guestExecResult
	if err := json.Unmarshal(output, &result); err != nil {
		return fmt.Errorf("decode router cleanup result: %w", err)
	}
	if result.Exited != 1 || result.ExitCode != 0 {
		return fmt.Errorf("router cleanup failed with exit code %d: %s", result.ExitCode, strings.TrimSpace(result.Error))
	}
	return nil
}
