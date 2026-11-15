package identity

import (
	"bytes"
	"encoding/pem"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"kvm/core/appprofile"
)

func TestEmbeddedConnectionNeverOverwritesPersistedSettings(t *testing.T) {
	tls := httptest.NewTLSServer(nil)
	defer tls.Close()
	p := appprofile.Profile{Version: 1, Server: tls.URL, CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tls.Certificate().Raw}))}
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "existing"}[existing], func(t *testing.T) {
			s, err := LoadPath(filepath.Join(t.TempDir(), "state.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if existing {
				s.Settings.Server = "https://original.example"
				s.Settings.CAFile = "original-ca.pem"
			}
			base := s.Settings
			key := bytes.Clone(s.Private)
			if err = s.Save(); err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyProfile(p); err != nil {
				t.Fatal(err)
			}
			if err = s.ApplyProfile(p); err != nil {
				t.Fatal(err)
			}
			s.Settings.UploadGiB = 7
			s.Settings.Clipboard = false
			s.LastID = "1234567890"
			if err = s.Save(); err != nil {
				t.Fatal(err)
			}
			if s.Settings.Server != p.Server || s.Settings.EmbeddedCA != p.CAPEM || !s.Settings.ServerLocked {
				t.Fatal("runtime overlay lost")
			}
			plain, err := LoadPath(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			if plain.Settings.Server != base.Server || plain.Settings.CAFile != base.CAFile {
				t.Fatal("persisted URL/CA overwritten")
			}
			if plain.Settings.ServerLocked || plain.Settings.EmbeddedCA != "" || plain.Settings.UploadGiB != 7 || plain.Settings.Clipboard || plain.LastID != s.LastID || !bytes.Equal(key, plain.Private) {
				t.Fatal("preferences/identity persistence incorrect")
			}
			attack := s.Settings
			attack.Server = "https://other.example"
			attack.CAFile = "other.pem"
			attack.ServerLocked = false
			attack.EmbeddedCA = "bad"
			effective := s.EnforceProfile(attack)
			if effective.Server != p.Server || effective.CAFile != "" || effective.EmbeddedCA != p.CAPEM || !effective.ServerLocked {
				t.Fatal("profile bypassed")
			}
		})
	}
}
