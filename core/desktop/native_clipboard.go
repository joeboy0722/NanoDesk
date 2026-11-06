package desktop

import (
	"encoding/json"
	"sync/atomic"
	"time"

	"kvm/core/auth"
	"kvm/core/clipboard"
	"kvm/core/input"
)

type nativeClipboardTransport interface {
	CaptureNativeClipboard(int64, func() bool) (clipboard.Snapshot, uint32, error)
	InstallNativeClipboard(clipboard.Snapshot, uint32, func() bool) (uint32, error)
	ReadNativeClipboardFile(string, int, int64, []byte, func() bool) (int, error)
	RelayNativeClipboardFiles(*clipboard.ClipboardManifest, func() bool, func(int, int64, []byte) (int, error))
}

type sharedNativeClipboard struct {
	source   *Client
	snapshot clipboard.Snapshot
	grant    func() bool
	sequence uint32
	revoked  *atomic.Bool
}

func (h *StreamHub) nativeClipboardAllowedLocked(c *Client) bool {
	return c.clipboardBatch && c.clipboardEnabled && c.role >= auth.RoleStandard && h.validLocked(c)
}

func (h *StreamHub) clipboardDeliveryAllowed(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.clipboardBatch {
		return h.nativeClipboardAllowedLocked(c)
	}
	return h.clipboardAllowedLocked(c)
}

func (h *StreamHub) clipboardGrant(c *Client, generation uint64) func() bool {
	return func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return generation == c.clipboardGeneration && h.nativeClipboardAllowedLocked(c)
	}
}

func (h *StreamHub) NativeClipboard(c *Client, kind string, data []byte) bool {
	switch kind {
	case "clipboard_batch_hello":
		h.mu.Lock()
		valid := h.validLocked(c) && c.role >= auth.RoleStandard
		if valid {
			c.clipboardBatch = true
		}
		h.mu.Unlock()
		if valid {
			c.safeSendJSON(map[string]any{"type": "native_clipboard_batch_ready"})
		}
	case "clipboard_batch", "clipboard_batch_paste", "clipboard_batch_key":
		h.mu.Lock()
		allowed := h.nativeClipboardAllowedLocked(c)
		generation := c.clipboardGeneration
		h.mu.Unlock()
		if !allowed {
			return true
		}
		var message clipboard.Snapshot
		if json.Unmarshal(data, &message) != nil {
			return true
		}
		if !message.Empty() {
			if !h.installSharedClipboard(c, generation, message) {
				if kind != "clipboard_batch" {
					c.safeSendJSON(map[string]any{"type": "operation_error", "message": "剪貼簿更新失敗，請重試貼上"})
				}
				return true
			}
		}
		if kind != "clipboard_batch" {
			h.inputMu.Lock()
			defer h.inputMu.Unlock()
			if h.isController(c) && !input.GetMasterOverride().IsOverridden() {
				if kind == "clipboard_batch_paste" {
					h.reportInputError(c, h.inputCtrl.SendKey(0x11, true))
				}
				h.reportInputError(c, h.inputCtrl.SendKey(0x56, true))
				h.reportInputError(c, h.inputCtrl.SendKey(0x56, false))
				if kind == "clipboard_batch_paste" {
					h.reportInputError(c, h.inputCtrl.SendKey(0x11, false))
				}
			}
		}
	default:
		return false
	}
	return true
}

func (h *StreamHub) installSharedClipboard(c *Client, generation uint64, snapshot clipboard.Snapshot) bool {
	transport, ok := c.conn.(nativeClipboardTransport)
	if !ok || snapshot.Files != nil && snapshot.Files.Validate(maxUploadBytes.Load()) != nil {
		return false
	}
	h.nativeClipboardMu.Lock()
	defer h.nativeClipboardMu.Unlock()
	sessionGrant := h.clipboardGrant(c, generation)
	revoked := &atomic.Bool{}
	grant := func() bool { return !revoked.Load() && sessionGrant() }
	if !grant() {
		return false
	}
	seq, err := transport.InstallNativeClipboard(snapshot, clipboard.GetSequenceNumber(), grant)
	if err != nil {
		return false
	}
	if h.sharedClipboard.revoked != nil {
		h.sharedClipboard.revoked.Store(true)
	}
	h.sharedClipboard = sharedNativeClipboard{source: c, snapshot: snapshot, grant: grant, sequence: seq, revoked: revoked}
	h.mu.Lock()
	c.clipboardSequence = seq
	for client := range h.clients {
		if client != c {
			client.clipboardSequence = seq - 1
		}
	}
	h.mu.Unlock()
	return true
}

func (h *StreamHub) watchNativeClipboard() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			h.publishSharedClipboard()
		}
	}
}

func (h *StreamHub) publishSharedClipboard() {
	h.nativeClipboardMu.Lock()
	defer h.nativeClipboardMu.Unlock()
	seq := clipboard.GetSequenceNumber()
	shared := h.sharedClipboard
	if shared.sequence != seq {
		if shared.revoked != nil {
			shared.revoked.Store(true)
		}
		shared = sharedNativeClipboard{}
		h.sharedClipboard = shared
	}
	h.mu.Lock()
	clients := make(map[*Client]uint64)
	for c := range h.clients {
		if h.nativeClipboardAllowedLocked(c) && c.clipboardSequence != seq {
			clients[c] = c.clipboardGeneration
		}
	}
	h.mu.Unlock()
	for c, generation := range clients {
		transport, ok := c.conn.(nativeClipboardTransport)
		if !ok {
			continue
		}
		grant := h.clipboardGrant(c, generation)
		var snapshot clipboard.Snapshot
		if shared.source != nil {
			if !shared.grant() {
				continue
			}
			snapshot = shared.snapshot
			if manifest := snapshot.Files; manifest != nil {
				source := shared.source.conn.(nativeClipboardTransport)
				transport.RelayNativeClipboardFiles(manifest, grant, func(item int, offset int64, data []byte) (int, error) {
					return source.ReadNativeClipboardFile(manifest.ID, item, offset, data, func() bool {
						return grant() && shared.grant() && clipboard.GetSequenceNumber() == seq
					})
				})
			} else {
				transport.RelayNativeClipboardFiles(nil, nil, nil)
			}
		} else if !clipboard.IsRemoteSequence(seq) {
			var observed uint32
			var err error
			snapshot, observed, err = transport.CaptureNativeClipboard(maxUploadBytes.Load(), grant)
			if err != nil || observed != seq {
				continue
			}
		}
		if !grant() || clipboard.GetSequenceNumber() != seq {
			continue
		}
		if !snapshot.Empty() {
			if err := c.safeSendJSON(map[string]any{"type": "clipboard_batch", "batch": snapshot.Batch, "files": snapshot.Files}); err != nil {
				continue
			}
		}
		h.mu.Lock()
		if c.clipboardGeneration == generation {
			c.clipboardSequence = seq
		}
		h.mu.Unlock()
	}
}
