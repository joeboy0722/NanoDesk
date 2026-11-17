package streamtrace

/*
#include <stdlib.h>
*/
import "C"
import "sync"

var nativeIDs sync.Map

func NativeObject(raw uint64) uint64 {
	if raw == 0 {
		return 0
	}
	if id, ok := nativeIDs.Load(raw); ok {
		return id.(uint64)
	}
	id, _ := nativeIDs.LoadOrStore(raw, ID())
	return id.(uint64)
}

//export ND_StreamTrace
func ND_StreamTrace(stage *C.char, object C.ulonglong, frame C.ulonglong, duration C.longlong, value C.longlong, result C.longlong) {
	Emit(Event{Stage: C.GoString(stage), Object: NativeObject(uint64(object)), Frame: uint64(frame), DurationUS: int64(duration), Value: int64(value), Result: int64(result)})
}

//export ND_StreamTraceName
func ND_StreamTraceName(name *C.char, object C.ulonglong) {
	Emit(Event{Stage: "encoder.mft", Object: NativeObject(uint64(object)), Name: C.GoString(name)})
}
