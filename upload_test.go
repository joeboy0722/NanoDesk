package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

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

func TestUploadConfiguredLimitEnforced(t *testing.T) {
	original := maxUploadBytes.Load()
	t.Cleanup(func() { maxUploadBytes.Store(original) })
	maxUploadBytes.Store(10 * gibibyte)
	h := testHub(t)
	r := httptest.NewRequest(http.MethodPost, "/upload", nil)
	r.ContentLength = maxUploadBytes.Load() + 1
	w := httptest.NewRecorder()
	h.handleUpload(w, r)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "10 GiB") {
		t.Fatal("configured limit not enforced", w.Code, w.Body)
	}

	// A scaled limit tests streaming enforcement without writing multi-GiB files.
	c := testClient(t, h, 2)
	dir := t.TempDir()
	r = uploadRequest(t, "large.bin", strings.Repeat("x", 1024))
	r.ContentLength = -1 // Cannot rely on Content-Length for streamed requests.
	w = httptest.NewRecorder()
	h.receiveUpload(w, r, c.token, dir, 512)
	if w.Code == http.StatusOK {
		t.Fatal("streaming request bypassed limit")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("oversized upload left files", files, err)
	}
}
