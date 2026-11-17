package engine

import (
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"kvm/core/streamtrace"
	"strings"
)

type traceFactory struct{}

func (traceFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	return &traceInterceptor{}, nil
}

type traceInterceptor struct{ interceptor.NoOp }

func (t *traceInterceptor) BindLocalStream(info *interceptor.StreamInfo, w interceptor.RTPWriter) interceptor.RTPWriter {
	if !strings.EqualFold(info.MimeType, "video/H264") {
		return w
	}
	object := streamtrace.ID()
	return interceptor.RTPWriterFunc(func(h *rtp.Header, b []byte, a interceptor.Attributes) (int, error) {
		done := streamtrace.Span("rtp.send_write", object, uint64(h.Timestamp))
		n, err := w.Write(h, b, a)
		result := int64(0)
		if err != nil {
			result = 1
		}
		done(int64(n), result)
		streamtrace.Emit(streamtrace.Event{Stage: "rtp.send", Object: object, Frame: uint64(h.Timestamp), Value: int64(h.SequenceNumber), Result: int64(h.SSRC)})
		return n, err
	})
}
