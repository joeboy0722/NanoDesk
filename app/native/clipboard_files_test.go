package native

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestOLEClipboardReadsFileOnDemand(t *testing.T) {
	var calls atomic.Int32
	result := testClipboardObject(func(index int, offset int64, data []byte) (int, error) {
		calls.Add(1)
		if index != 1 || offset != 0 || len(data) != 5 {
			t.Errorf("unexpected read: item=%d offset=%d size=%d", index, offset, len(data))
		}
		return copy(data, "hello"), nil
	})
	if result != 0 || calls.Load() != 1 {
		t.Fatalf("OLE verification failed at step %d, read calls %d", result, calls.Load())
	}
}

func TestOLEClipboardReadDoesNotNeedSTAProgress(t *testing.T) {
	result := testClipboardStreamWhileSTAWaits(func(index int, offset int64, data []byte) (int, error) {
		time.Sleep(100 * time.Millisecond)
		return copy(data, "hello"), nil
	})
	if result != 0 {
		t.Fatalf("MTA stream needed UI progress: step %d", result)
	}
}
