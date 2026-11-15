package engine

import "github.com/pion/webrtc/v4"

// VideoDiagnostics returns transport counters without desktop or clipboard data.
func (e *Engine) VideoDiagnostics(sid string) map[string]any {
	p := e.get(sid)
	if p == nil {
		return nil
	}
	result := map[string]any{}
	result["read"] = map[string]any{"packets": p.diagnosticPackets.Load(), "bytes": p.diagnosticBytes.Load(), "samples": p.diagnosticSamples.Load(), "keys": p.diagnosticKeys.Load()}
	for _, stat := range p.pc.GetStats() {
		switch v := stat.(type) {
		case webrtc.InboundRTPStreamStats:
			result["rtp"] = map[string]any{"packets": v.PacketsReceived, "bytes": v.BytesReceived, "lost": v.PacketsLost}
		case webrtc.ICECandidatePairStats:
			if v.Nominated {
				result["transport"] = map[string]any{"sent": v.BytesSent, "received": v.BytesReceived}
			}
		}
	}
	return result
}
