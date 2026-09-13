// GuiService is the service boundary for lantern-gui.
//
// Every method is a thin proxy over lanternd's v1 API via Client; no
// transfer logic lives here, so the GUI can never drift from the CLI,
// the SDKs, or api/openapi.yaml. The Fyne window calls these methods from
// button handlers; the console fallback uses Client directly.
package main

import "encoding/base64"

// guiVersion marks the spike; real releases inject this via ldflags.
const guiVersion = "0.1.0-spike"

// GuiService wraps the daemon client for the desktop shell.
type GuiService struct {
	client *Client
}

// NewGuiService builds the bound service for baseURL/token (flags or env).
func NewGuiService(baseURL, token string) *GuiService {
	return &GuiService{client: NewClient(baseURL, token)}
}

// AppInfo returns static header metadata for the frontend.
func (s *GuiService) AppInfo() AppInfo {
	return AppInfo{Name: "Lantern", Version: guiVersion}
}

// ShareFile advertises a local file and returns its share record.
func (s *GuiService) ShareFile(path string) (Transfer, error) {
	return s.client.ShareFile(path)
}

// ShareFileWithTTL advertises a local file with an expiry in seconds
// (0 = daemon default) and returns its share record.
func (s *GuiService) ShareFileWithTTL(path string, ttlSeconds int64) (Transfer, error) {
	return s.client.ShareFileWithTTL(path, ttlSeconds)
}

// FetchCode pulls a share code into outDir.
func (s *GuiService) FetchCode(code, outDir string) (Transfer, error) {
	return s.client.FetchCode(code, outDir)
}

// ListTransfers returns live transfers, optionally filtered by kind.
func (s *GuiService) ListTransfers(kind string) ([]Transfer, error) {
	return s.client.ListTransfers(kind)
}

// GetTransfer returns one transfer snapshot for progress polling.
func (s *GuiService) GetTransfer(id string) (Transfer, error) {
	return s.client.GetTransfer(id)
}

// CancelTransfer cancels or revokes a transfer.
func (s *GuiService) CancelTransfer(id string) error {
	return s.client.CancelTransfer(id)
}

// ListHistory returns recent terminal transfers.
func (s *GuiService) ListHistory() ([]Transfer, error) {
	return s.client.ListHistory()
}

// GetStatus returns daemon and node status.
func (s *GuiService) GetStatus() (Status, error) {
	return s.client.GetStatus()
}

// ListPeers returns currently connected peers.
func (s *GuiService) ListPeers() ([]Peer, error) {
	return s.client.ListPeers()
}

// UploadData stores base64-encoded file bytes daemon-side and returns the
// daemon path to pass to ShareFile. The argument stays a string (not []byte)
// for easy handoff from UI callbacks; decoding happens here.
func (s *GuiService) UploadData(fileName, base64Data string) (Upload, error) {
	raw, err := base64.StdEncoding.DecodeString(base64Data)
	if err != nil {
		return Upload{}, err
	}
	return s.client.UploadFile(fileName, raw)
}
