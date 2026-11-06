package clipboard

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFileOffer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "資料")
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "test.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileOffer([]string{root}, 4); err == nil {
		t.Fatal("offer exceeded the byte limit")
	}
	offer, err := NewFileOffer([]string{root}, 5)
	if err != nil {
		t.Fatal(err)
	}
	items := offer.Items()
	if len(items) != 3 || !items[0].Directory || !items[1].Directory || items[2].Name != filepath.Join("資料", "test.txt") {
		t.Fatalf("unexpected manifest: %+v", items)
	}
	file, err := offer.Open(2)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(file)
	file.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("unexpected file: %q, %v", data, err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if file, err := offer.Open(2); err == nil {
		file.Close()
		t.Fatal("changed file was accepted")
	}
}

func TestValidateFileItems(t *testing.T) {
	valid := []FileItem{{Name: "資料", Directory: true}, {Name: "資料\\test.txt", Size: 5}}
	if err := ValidateFileItems(valid, 5); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"..\\outside", "C:\\file", "資料\\..\\outside", "資料\\NUL.txt", "資料\\test.txt:stream", "missing\\test.txt"} {
		if err := ValidateFileItems([]FileItem{valid[0], {Name: name}}, 5); err == nil {
			t.Errorf("accepted invalid file name %q", name)
		}
	}
	if err := ValidateFileItems(valid, 4); err == nil {
		t.Error("accepted oversized manifest")
	}
}
