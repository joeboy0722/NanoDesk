package appprofile

import (
	"bytes"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func testProfile(t *testing.T) Profile {
	t.Helper()
	s := httptest.NewTLSServer(nil)
	defer s.Close()
	return Profile{Version: 1, Server: s.URL, CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}))}
}

func TestExportResourceRoundTrip(t *testing.T) {
	p := testProfile(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(t.TempDir(), "template.exe")
	if err = os.WriteFile(template, b, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "configured.exe")
	if err = Export(template, destination, p); err == nil {
		t.Fatal("unmarked template accepted")
	}
	if err = update(template, markerID, []byte(Marker)); err != nil {
		t.Fatal(err)
	}
	if err = update(template, 30003, []byte("preserve other resources")); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(template)
	if got, err := Read(template); err != nil || got != nil {
		t.Fatalf("standard mode: %v %v", got, err)
	}
	if err = Export(template, destination, p); err != nil {
		t.Fatal(err)
	}
	got, err := Read(destination)
	if err != nil || got == nil || *got != p {
		t.Fatalf("round trip: %v %v", got, err)
	}
	other, ok, err := resource(destination, 30003)
	if err != nil || !ok || string(other) != "preserve other resources" {
		t.Fatal("other resource lost", err)
	}
	after, _ := os.ReadFile(template)
	if !bytes.Equal(before, after) {
		t.Fatal("source template changed")
	}
	if err = Export(template, template, p); err == nil {
		t.Fatal("template overwrite accepted")
	}
	p.Server = "https://replacement.example:8443"
	if err = Export(destination, template, p); err != nil {
		t.Fatal(err)
	}
	got, err = Read(template)
	if err != nil || got == nil || *got != p {
		t.Fatal("re-export failed", err)
	}
	if err = update(destination, profileID, []byte("invalid")); err != nil {
		t.Fatal(err)
	}
	if _, err = Read(destination); err == nil {
		t.Fatal("corrupt profile fell back to standard mode")
	}
}

func TestProfileValidation(t *testing.T) {
	p := testProfile(t)
	for _, url := range []string{"http://host", "https://user:pass@host", "https://host/path", "https://host?", "https://host#x", "https://host:99999"} {
		bad := p
		bad.Server = url
		if bad.Validate() == nil {
			t.Errorf("accepted %s", url)
		}
	}
	for _, ca := range []string{"", " \r\n", p.CAPEM + "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----", "garbage\n" + p.CAPEM} {
		bad := p
		bad.CAPEM = ca
		if bad.Validate() == nil {
			t.Fatal("invalid public CA accepted")
		}
	}
}
