package native

/*
#cgo CXXFLAGS: -std=c++17 -O2 -DUNICODE -D_UNICODE
#cgo LDFLAGS: -static -static-libgcc -static-libstdc++ -lcomctl32 -lcomdlg32 -lgdi32 -luser32 -lole32 -loleaut32 -lmfplat -lmfuuid -ld3d11 -ldxgi -luuid
#include "native.h"
#include <stdlib.h>
*/
import "C"

import (
	"kvm/core/identity"
	"kvm/core/streamtrace"
	"unsafe"
)

type Event struct {
	Kind, A, B    int
	X, Y          float64
	Session, Text string
}

func str(s string) (*C.char, func()) {
	p := C.CString(s)
	return p, func() { C.free(unsafe.Pointer(p)) }
}
func Init() bool { return C.ND_Init() != 0 }
func Poll() (Event, int) {
	var v C.NDEvent
	done := streamtrace.Span("ui.poll", 0, 0)
	result := int(C.ND_Poll(&v))
	done(0, int64(result))
	return Event{int(v.kind), int(v.a), int(v.b), float64(v.x), float64(v.y), C.GoString(&v.session[0]), C.GoString(&v.text[0])}, result
}
func MainText(which int, s string) { p, f := str(s); defer f(); C.ND_MainText(C.int(which), p) }
func Settings(s identity.Settings) {
	a, fa := str(s.Server)
	defer fa()
	b, fb := str(s.CAFile)
	defer fb()
	v, fv := str(s.ViewPassword)
	defer fv()
	st, fs := str(s.StandardPassword)
	defer fs()
	ad, fd := str(s.AdminPassword)
	defer fd()
	C.ND_Settings(a, b, boolean(s.Unattended), v, st, ad, C.int64_t(s.UploadGiB), boolean(s.Clipboard), boolean(s.RelayOnly), boolean(s.RelayTLSOnly), boolean(s.ServerLocked))
}
func boolean(v bool) C.int {
	if v {
		return 1
	}
	return 0
}
func Open(sid, id string) {
	a, fa := str(sid)
	defer fa()
	b, fb := str(id)
	defer fb()
	C.ND_Open(a, b)
}
func Text(sid, s string) {
	a, fa := str(sid)
	defer fa()
	b, fb := str(s)
	defer fb()
	C.ND_RemoteText(a, b)
}
func Authenticated(sid string, role int) {
	p, f := str(sid)
	defer f()
	C.ND_Authenticated(p, C.int(role))
}
func ClearMonitors(sid string) { p, f := str(sid); defer f(); C.ND_ClearMonitors(p) }
func Monitor(sid string, index, number, w, h int, primary, selected bool) {
	p, f := str(sid)
	defer f()
	C.ND_Monitor(p, C.int(index), C.int(number), C.int(w), C.int(h), boolean(primary), boolean(selected))
}
func Control(sid string, controller, local bool) {
	p, f := str(sid)
	defer f()
	C.ND_Control(p, boolean(controller), boolean(local))
}
func Approval(sid, request, id string) {
	a, fa := str(sid)
	defer fa()
	b, fb := str(request)
	defer fb()
	c, fc := str(id)
	defer fc()
	C.ND_Approval(a, b, c)
}
func CloseApproval(sid string) { p, f := str(sid); defer f(); C.ND_CloseApproval(p) }
func Close(sid string)         { p, f := str(sid); defer f(); C.ND_Close(p) }
func Error(s string)           { p, f := str(s); defer f(); C.ND_Error(p) }
func Frame(sid string, b []byte) int {
	if len(b) == 0 {
		return 0
	}
	p, f := str(sid)
	defer f()
	done := streamtrace.Span("viewer.decode_total", 0, 0)
	result := int(C.ND_Frame(p, (*C.uchar)(unsafe.Pointer(&b[0])), C.int(len(b))))
	done(int64(len(b)), int64(result))
	return result
}
func Panel(sid, s string, members bool) {
	p, f := str(sid)
	defer f()
	t, ft := str(s)
	defer ft()
	C.ND_Panel(p, t, boolean(members))
}
func Laser(sid, id, name, color string, x, y float64) {
	p, f := str(sid)
	defer f()
	i, fi := str(id)
	defer fi()
	n, fn := str(name)
	defer fn()
	c, fc := str(color)
	defer fc()
	C.ND_Laser(p, i, n, c, C.double(x), C.double(y))
}

func testDecoderCreate() unsafe.Pointer { return C.ND_TestDecoderCreate() }
func testDecoderClose(p unsafe.Pointer) { C.ND_TestDecoderClose(p) }
func testDecoderFeed(p unsafe.Pointer, b []byte) (int, int, int) {
	var w, h C.int
	r := C.ND_TestDecoderFeed(p, (*C.uchar)(unsafe.Pointer(&b[0])), C.int(len(b)), &w, &h)
	return int(r), int(w), int(h)
}

func Clipboard(sid string, on bool) { p, f := str(sid); defer f(); C.ND_Clipboard(p, boolean(on)) }

func Forget(sid string) { p, f := str(sid); defer f(); C.ND_Forget(p) }

func InboundBegin() { C.ND_InboundBegin() }
func InboundAdd(sid, device, role string, controller bool) {
	a, fa := str(sid)
	defer fa()
	b, fb := str(device)
	defer fb()
	c, fc := str(role)
	defer fc()
	C.ND_InboundAdd(a, b, c, boolean(controller))
}
func InboundEnd() { C.ND_InboundEnd() }
func ChatText(sid, message string, local bool) {
	a, fa := str(sid)
	defer fa()
	b, fb := str(message)
	defer fb()
	C.ND_ChatText(a, b, boolean(local))
}
func IsActive(sid string) bool {
	p, free := str(sid)
	defer free()
	return C.ND_IsActive(p) != 0
}

func BindSession(pending, sid string) bool {
	a, fa := str(pending)
	defer fa()
	b, fb := str(sid)
	defer fb()
	return C.ND_BindSession(a, b) != 0
}

func ChatError(sid, message string) {
	a, fa := str(sid)
	defer fa()
	b, fb := str(message)
	defer fb()
	C.ND_ChatText(a, b, -1)
}

func FrameTraced(sid string, b []byte, object, frame uint64) int {
	if streamtrace.Enabled() {
		p, f := str(sid)
		raw := C.ND_TraceFrame(p, C.ulonglong(frame))
		f()
		streamtrace.Emit(streamtrace.Event{Stage: "viewer.decoder_link", Object: object, Frame: frame, Value: int64(streamtrace.NativeObject(uint64(raw)))})
	}
	return Frame(sid, b)
}

func testDecoderFeedTraced(p unsafe.Pointer, b []byte, object, frame uint64) (int, int, int) {
	if streamtrace.Enabled() {
		C.ND_TestDecoderTrace(p, C.ulonglong(frame))
		streamtrace.Emit(streamtrace.Event{Stage: "viewer.decoder_link", Object: object, Frame: frame, Value: int64(streamtrace.NativeObject(uint64(uintptr(p))))})
	}
	return testDecoderFeed(p, b)
}

func testShowWindow() bool { return C.ND_TestShowWindow() != 0 }
func testRenderCounts(sid string) (uint64, uint64) {
	p, f := str(sid)
	defer f()
	return uint64(C.ND_TestPaintCount(p)), uint64(C.ND_TestPresentCount(p))
}

func videoOpen(sid string, generation uint64) {
	p, f := str(sid)
	defer f()
	C.ND_VideoOpen(p, C.ulonglong(generation))
}
func videoClose(sid string, generation uint64) {
	p, f := str(sid)
	defer f()
	C.ND_VideoClose(p, C.ulonglong(generation))
}
func videoPublish(sid string, generation uint64, decoder unsafe.Pointer) int {
	p, f := str(sid)
	defer f()
	return int(C.ND_VideoPublish(p, C.ulonglong(generation), decoder))
}
func decoderTag(p unsafe.Pointer, frame uint64) { C.ND_TestDecoderTrace(p, C.ulonglong(frame)) }
func decoderReset(p unsafe.Pointer)             { C.ND_DecoderReset(p) }

func testVideoState(sid string) (uint64, uint64, uint64) {
	p, f := str(sid)
	defer f()
	return uint64(C.ND_TestShownFrame(p)), uint64(C.ND_TestPendingVideo(p)), uint64(C.ND_TestVideoPool(p))
}
func testVideoWindow(sid string, mode int) {
	p, f := str(sid)
	defer f()
	C.ND_TestVideoWindow(p, C.int(mode))
}

func SourceState(sid string, reused bool) {
	p, f := str(sid)
	defer f()
	var value C.int
	if reused {
		value = 1
	}
	C.ND_SourceState(p, value)
}
