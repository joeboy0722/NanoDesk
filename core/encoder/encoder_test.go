package encoder

import "testing"

func TestNativeEncoderLifecycle(t *testing.T) {
	if err := CheckMediaFoundationAvailable(); err != nil {
		t.Skip(err)
	}
	e, err := NewH264Encoder(640, 480, 30, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, _, err = e.Encode(make([]byte, 4)); err == nil {
		t.Fatal("short frame accepted")
	}
	pixels := make([]byte, 640*480*4)
	keys, frames := 0, 0
	for i := 0; i < 12; i++ {
		if i == 6 {
			e.RequestKeyframe()
		}
		data, key, err := e.Encode(pixels)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 0 {
			frames++
			if key {
				keys++
			}
		}
	}
	if frames == 0 || keys == 0 {
		t.Fatalf("no valid stream: frames=%d keys=%d", frames, keys)
	}
	e.Close()
	e.Close()
	if _, _, err = e.Encode(pixels); err == nil {
		t.Fatal("closed encoder accepted input")
	}
}
