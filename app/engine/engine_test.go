package engine

import (
	"path/filepath"
	"testing"

	"kvm/core/identity"
)

func TestSettingsValidationAndNoUnattendedByDefault(t *testing.T) {
	s, err := identity.LoadPath(filepath.Join(t.TempDir(), "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, _, ok := e.mgr.AuthenticateByPassword("123456", "test"); ok {
		t.Fatal("unexpected unattended password")
	}
	cfg := s.Settings
	cfg.Server = "http://example.com"
	if err = e.SaveSettings(cfg); err == nil {
		t.Fatal("plaintext signaling accepted")
	}
	cfg.Server = "https://example.com"
	cfg.Unattended = true
	if err = e.SaveSettings(cfg); err == nil {
		t.Fatal("empty unattended configuration accepted")
	}
	cfg.AdminPassword = "a-strong-test-password"
	if err = e.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	_, token, ok := e.mgr.AuthenticateByPassword(cfg.AdminPassword, "test")
	if !ok {
		t.Fatal("password not applied")
	}
	cfg.Unattended = false
	if err = e.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok = e.mgr.ValidateToken(token); ok {
		t.Fatal("saving settings retained authorization")
	}
}
func TestSignalURLValidation(t *testing.T) {
	for _, s := range []string{"http://host", "https://user:pass@host", "https://host/path", "https://host?secret=1", "https://host#fragment", "garbage"} {
		if _, e := signalURL(s); e == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if s, e := signalURL("https://example.com:443/"); e != nil || s != "wss://example.com:443/signal" {
		t.Fatal(s, e)
	}
}
