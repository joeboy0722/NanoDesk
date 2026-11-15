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

func TestDynamicFramesRespectBitrateBudget(t *testing.T) {
	const width, height, fps, kbps = 640, 480, 30, 1500
	e, err := NewH264Encoder(width, height, fps, kbps)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	pixels := make([]byte, width*height*4)
	var random uint32 = 123456789
	bytes, frames := 0, 0
	for frame := 0; frame < 75; frame++ {
		// A changing video panel on a desktop. Full-screen independent white
		// noise has an irreducible coding cost; source pacing handles that case.
		for i := 0; i < len(pixels); i += 4 {
			pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = 80, 100, 120, 255
		}
		for y := 0; y < height/4; y++ {
			for x := 0; x < width/2; x++ {
				i := (y*width + x) * 4
				random ^= random << 13
				random ^= random >> 17
				random ^= random << 5
				pixels[i], pixels[i+1], pixels[i+2], pixels[i+3] = byte(random), byte(random>>8), byte(random>>16), 255
			}
		}
		data, _, err := e.Encode(pixels)
		if err != nil {
			t.Fatal(err)
		}
		if frame >= 15 && len(data) > 0 {
			bytes += len(data)
			frames++
		}
	}
	// After warm-up allow 2x the requested average plus one small burst;
	// this detects the previous unlimited VBR/QP floor, not instant CBR peaks.
	limit := kbps*1000/8*2*2 + 64*1024
	t.Logf("dynamic video: output_frames=%d bytes=%d budget_limit=%d", frames, bytes, limit)
	if frames < 50 || bytes > limit {
		t.Fatalf("dynamic stream exceeded bitrate budget: frames=%d bytes=%d limit=%d", frames, bytes, limit)
	}
}
