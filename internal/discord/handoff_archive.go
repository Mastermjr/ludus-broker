package discord

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/fs"
	"time"

	"github.com/Mastermjr/ludus-viberange-broker/internal/ludus"
)

type handoffAsset struct {
	name string
	mode fs.FileMode
	data []byte
}

func wireGuardHandoffArchive(slot string, profile []byte, peer ludus.WireGuardPeer) ([]byte, error) {
	if err := validateHandoffMetadata(slot, peer); err != nil {
		return nil, err
	}
	if len(profile) == 0 {
		return nil, fmt.Errorf("WireGuard profile is empty")
	}
	linuxGuide, err := linuxHandoffGuide(slot, peer)
	if err != nil {
		return nil, err
	}
	linuxScript, err := linuxAcceptanceScript(slot, peer)
	if err != nil {
		return nil, err
	}
	windowsGuide, err := windowsHandoffGuide(slot, peer)
	if err != nil {
		return nil, err
	}
	windowsScript, err := windowsAcceptanceScript(slot, peer)
	if err != nil {
		return nil, err
	}

	prefix := "viberange-" + slot
	assets := []handoffAsset{
		{name: prefix + ".conf", mode: 0o600, data: profile},
		{name: prefix + "-linux-setup.md", mode: 0o600, data: linuxGuide},
		{name: prefix + "-acceptance.sh", mode: 0o700, data: linuxScript},
		{name: prefix + "-windows-setup.md", mode: 0o600, data: windowsGuide},
		{name: prefix + "-acceptance.ps1", mode: 0o700, data: windowsScript},
	}

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, asset := range assets {
		header := &zip.FileHeader{Name: asset.name, Method: zip.Deflate}
		header.SetMode(asset.mode)
		// Avoid embedding local clock or filesystem metadata in participant archives.
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("create handoff archive entry: %w", createErr)
		}
		if _, writeErr := entry.Write(asset.data); writeErr != nil {
			_ = writer.Close()
			return nil, fmt.Errorf("write handoff archive entry: %w", writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close handoff archive: %w", err)
	}
	return buffer.Bytes(), nil
}
