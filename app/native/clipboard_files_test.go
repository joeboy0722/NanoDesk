package native

import "testing"

func TestOLEClipboardReadsFileOnDemand(t *testing.T) {
	calls := 0
	result := testClipboardObject(func(index int, offset int64, data []byte) (int, error) {
		calls++
		if index != 1 || offset != 0 || len(data) != 5 {
			t.Errorf("unexpected read: item=%d offset=%d size=%d", index, offset, len(data))
		}
		return copy(data, "hello"), nil
	})
	if result != 0 || calls != 1 {
		t.Fatalf("OLE verification failed at step %d, read calls %d", result, calls)
	}
}
