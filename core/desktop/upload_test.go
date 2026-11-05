package desktop

import "testing"

func TestUploadLimitConfiguration(t *testing.T) {
	for _, gib := range []int64{1, 10, 100, 8388607} {
		limit, err := uploadLimitBytes(gib)
		if err != nil || limit != gib*gibibyte {
			t.Fatalf("%d GiB: bytes=%d err=%v", gib, limit, err)
		}
	}
	for _, gib := range []int64{-1, 0, 8388608, 1 << 62} {
		if _, err := uploadLimitBytes(gib); err == nil {
			t.Fatalf("accepted invalid limit %d", gib)
		}
	}
}
