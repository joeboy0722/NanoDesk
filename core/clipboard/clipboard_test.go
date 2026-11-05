package clipboard

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"
)

func TestDIBBoundsAndOrientation(t *testing.T) {
	data := make([]byte, 48)
	binary.LittleEndian.PutUint32(data, 40)
	binary.LittleEndian.PutUint32(data[4:], 1)
	binary.LittleEndian.PutUint32(data[8:], 2)
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 24)
	copy(data[40:], []byte{255, 0, 0, 0, 0, 0, 255, 0})
	img, err := decodeDIB(data)
	if err != nil {
		t.Fatal(err)
	}
	if c := img.RGBAAt(0, 0); c.R != 255 || c.B != 0 {
		t.Fatal("bottom-up image reversed", c)
	}
	if _, err = decodeDIB(data[:47]); err == nil {
		t.Fatal("truncated pixels accepted")
	}
	for _, offset := range []int{0, 4, 8, 16, 32} {
		bad := append([]byte(nil), data...)
		binary.LittleEndian.PutUint32(bad[offset:], 0x80000000)
		if _, err = decodeDIB(bad); err == nil {
			t.Fatal("invalid header accepted", offset)
		}
	}
	if validImageSize(16384, 16384) {
		t.Fatal("pixel limit missing")
	}
}

func TestRejectOversizedPNGBeforeClipboard(t *testing.T) {
	// Advertise excessive dimensions without allocating the decoded bitmap.
	var buf bytes.Buffer
	png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1)))
	data := buf.Bytes()
	binary.BigEndian.PutUint32(data[16:], 16384)
	binary.BigEndian.PutUint32(data[20:], 16384)
	binary.BigEndian.PutUint32(data[29:], crc32.ChecksumIEEE(data[12:29]))
	if err := WriteImagePNG(data); err == nil {
		t.Fatal("pixel limit missing")
	}
	if err := WriteImagePNG(data[:12]); err == nil {
		t.Fatal("invalid PNG accepted")
	}
	if err := WriteImagePNG(make([]byte, 7*1024*1024+1)); err == nil {
		t.Fatal("byte limit missing")
	}
}
