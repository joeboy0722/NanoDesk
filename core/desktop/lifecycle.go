package desktop

func UploadLimitBytes(gib int64) (int64, error) { return uploadLimitBytes(gib) }
func SetUploadLimitBytes(bytes int64)           { maxUploadBytes.Store(bytes) }
func (h *StreamHub) NotifyUploadLimit()         { h.broadcastUploadLimit() }
func (h *StreamHub) SetUI(ui HostUI)            { h.hostGUI = ui }
func (h *StreamHub) Close() {
	h.startOnce.Do(func() {})
	h.stopOnce.Do(func() {
		if h.done != nil {
			close(h.done)
		}
		if h.stopWatcher != nil {
			h.stopWatcher()
		}
	})
	h.kickAllClients()
	h.streamMu.Lock()
	defer h.streamMu.Unlock()
	if h.encoder != nil {
		h.encoder.Close()
		h.encoder = nil
	}
	if h.capEngine != nil {
		h.capEngine.Close()
	}
}
