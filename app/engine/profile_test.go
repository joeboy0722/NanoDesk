package engine

import (
	"crypto/tls"
	"encoding/pem"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"kvm/core/appprofile"
	"kvm/core/identity"
)

func TestLockedProfileSaveAndTLS(t *testing.T) {
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	p := appprofile.Profile{Version: 1, Server: server.URL, CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))}
	s, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	s.Settings.Server, s.Settings.CAFile = "https://original.example", "original.pem"
	if err = s.ApplyProfile(p); err != nil {
		t.Fatal(err)
	}
	e, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	changed := e.Settings()
	changed.Server = "http://invalid"
	changed.CAFile = "missing.pem"
	changed.EmbeddedCA = ""
	changed.ServerLocked = false
	changed.UploadGiB = 9
	if err = e.SaveSettings(changed); err != nil {
		t.Fatal(err)
	}
	actual := e.Settings()
	if actual.Server != p.Server || actual.CAFile != "" || actual.EmbeddedCA != p.CAPEM || !actual.ServerLocked || actual.UploadGiB != 9 {
		t.Fatal("backend did not enforce overlay")
	}
	// The embedded certificate is used from memory, even if a stale path is supplied.
	actual.CAFile = "does-not-exist.pem"
	cfg, err := trustedTLS(actual)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := tls.Dial("tcp", strings.TrimPrefix(server.URL, "https://"), cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	plain, err := identity.LoadPath(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if plain.Settings.Server != "https://original.example" || plain.Settings.CAFile != "original.pem" || plain.Settings.UploadGiB != 9 {
		t.Fatal("saving preferences overwrote original network settings")
	}
}
