package desktop

func (h *StreamHub) broadcastSourceState(reused bool, lastFresh int64) {
	h.mu.Lock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		if h.validLocked(c) {
			clients = append(clients, c)
		}
	}
	h.mu.Unlock()
	for _, c := range clients {
		c.safeSendJSON(map[string]any{"type": "source_frame_state", "reused": reused, "last_fresh_us": lastFresh})
	}
}

// Caller owns h.mu. Wake idle streams when authorization changes, while keeping
// an active stream's existing bandwidth debt unless viewers transitioned to zero.
func (h *StreamHub) signalStreamLocked() {
	active := false
	for c := range h.clients {
		if h.validLocked(c) {
			active = true
			break
		}
	}
	if active != h.streamViewers {
		h.streamReset = true
	}
	h.streamViewers = active
	if h.streamWake != nil {
		select {
		case h.streamWake <- struct{}{}:
		default:
		}
	}
}
