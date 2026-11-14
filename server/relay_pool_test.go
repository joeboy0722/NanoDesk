package main

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
)

func TestRelayTLSRejectsMissingOrInvalidCredentials(t *testing.T) {
	_, server, _ := testHub(t)
	for _, headers := range []http.Header{
		{},
		{"X-Nanodesk-Channel": []string{"00"}, "X-Nanodesk-Key": []string{"00"}},
		{"X-Nanodesk-Channel": []string{"00000000000000000000000000000000"}, "X-Nanodesk-Key": []string{"0000000000000000000000000000000000000000000000000000000000000000"}},
	} {
		request, err := http.NewRequest(http.MethodGet, server.URL+"/relay", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header = headers
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("unauthorized relay response %d", response.StatusCode)
		}
	}
}

type relayLockCheckingWriter struct {
	*httptest.ResponseRecorder
	mu     *sync.Mutex
	locked bool
}

func (w *relayLockCheckingWriter) WriteHeader(status int) {
	if w.mu.TryLock() {
		w.mu.Unlock()
	} else {
		w.locked = true
	}
	w.ResponseRecorder.WriteHeader(status)
}

func TestRelayTLSUpgradeReleasesSignalingLock(t *testing.T) {
	h, _, _ := testHub(t)
	setupSharedPool(t, h)
	source := netip.MustParseAddr("127.0.0.1")
	candidates := h.relayPool.Candidates(source)
	binding := netip.MustParseAddrPort(candidates[0].BindAddress)
	session, err := h.relayPool.OpenSession(source, source, []netip.AddrPort{binding}, []netip.AddrPort{binding}, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	h.relayRequests["lock-check"] = &relayRequestState{session: session, generation: 1}
	request := httptest.NewRequest(http.MethodGet, "https://localhost/relay", nil)
	request.Header.Set("X-NanoDesk-Channel", hex.EncodeToString(session.Left.Channel[:]))
	request.Header.Set("X-NanoDesk-Key", hex.EncodeToString(session.Left.ReceiveKey[:]))
	writer := &relayLockCheckingWriter{ResponseRecorder: httptest.NewRecorder(), mu: &h.mu}
	// Deliberately omit the WebSocket upgrade headers. Even an error response
	// must be written after releasing the signaling lock.
	h.serveRelay(writer, request)
	if writer.locked {
		t.Fatal("TLS upgrade held the signaling lock during network output")
	}
}
