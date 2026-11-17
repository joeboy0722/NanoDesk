package engine

import (
	"bytes"
	"errors"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"reflect"
	"testing"
)

func TestTraceInterceptorPreservesPackets(t *testing.T) {
	header := rtp.Header{Version: 2, SSRC: 17, SequenceNumber: 65535, Timestamp: 0xfffffff0, Marker: true}
	payload := []byte{0x65, 1, 2, 3}
	original := header
	originalPayload := append([]byte(nil), payload...)
	wantErr := errors.New("injected writer error")
	called := false
	writer := interceptor.RTPWriterFunc(func(h *rtp.Header, b []byte, _ interceptor.Attributes) (int, error) {
		called = true
		if !reflect.DeepEqual(*h, original) || !bytes.Equal(b, originalPayload) {
			t.Fatal("packet changed")
		}
		return len(b), wantErr
	})
	wrapped := (&traceInterceptor{}).BindLocalStream(&interceptor.StreamInfo{MimeType: "video/H264"}, writer)
	n, err := wrapped.Write(&header, payload, nil)
	if !called || n != len(payload) || err != wantErr {
		t.Fatal("writer result changed")
	}
	if !reflect.DeepEqual(header, original) || !bytes.Equal(payload, originalPayload) {
		t.Fatal("input mutated")
	}
}
