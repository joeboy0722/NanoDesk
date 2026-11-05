package native

import (
	"kvm/core/encoder"
	"runtime"
	"testing"
)

func TestNativeH264RoundTrip(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	d := testDecoderCreate()
	if d == nil {
		t.Fatal("Media Foundation decoder unavailable")
	}
	defer testDecoderClose(d)
	enc, err := encoder.NewH264Encoder(640, 480, 30, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	pixels := make([]byte, 640*480*4)
	for i := 0; i < len(pixels); i += 4 {
		pixels[i] = 80
		pixels[i+1] = 130
		pixels[i+2] = 180
		pixels[i+3] = 255
	}
	frames := 0
	for i := 0; i < 15; i++ {
		b, _, err := enc.Encode(pixels)
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			continue
		}
		result, w, h := testDecoderFeed(d, b)
		if result < 0 {
			t.Fatalf("decode failed at frame %d (%d bytes)", i, len(b))
		}
		if result > 0 {
			frames++
			if w != 640 || h != 480 {
				t.Fatalf("unexpected dimensions %dx%d", w, h)
			}
		}
	}
	if frames == 0 {
		t.Fatal("no decoded frames")
	}
	// A monitor switch replaces the sender's encoder but keeps the receiving
	// decoder alive. 600 lines may be coded as 608 with a display aperture.
	enc.Close()
	next, err := encoder.NewH264Encoder(800, 600, 30, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	changed := false
	for i := 0; i < 15; i++ {
		b, _, err := next.Encode(make([]byte, 800*600*4))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			continue
		}
		result, w, h := testDecoderFeed(d, b)
		if result < 0 {
			t.Fatal("decoder failed after resolution change")
		}
		if result > 0 && w == 800 && h >= 600 && h <= 608 {
			changed = true
		}
	}
	if !changed {
		t.Fatal("decoder did not negotiate the new resolution")
	}
}
