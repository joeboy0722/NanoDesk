package main

import (
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kvm/core/appprofile"
)

func TestConfiguredAppExport(t *testing.T) {
	template := os.Getenv("NANODESK_TEST_APP_TEMPLATE")
	if template == "" {
		t.Skip("set NANODESK_TEST_APP_TEMPLATE to a built APP")
	}
	server := httptest.NewTLSServer(nil)
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "public.crt")
	b := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(ca, b, 0600); err != nil {
		t.Fatal(err)
	}
	s := serverSettings{Host: "127.0.0.1", HTTPSPort: 8443, Certificate: ca, CAFile: ca, PrivateKey: filepath.Join(dir, "private.key"), Database: filepath.Join(dir, "ids.db")}
	destination := filepath.Join(dir, "ready.exe")
	if err := exportConfiguredApp(s, template, destination); err != nil {
		t.Fatal(err)
	}
	p, err := appprofile.Read(destination)
	if err != nil || p == nil || p.Server != s.appURL() || p.CAPEM != string(b) {
		t.Fatal("export mismatch", p, err)
	}
	standard, err := appprofile.Read(template)
	if err != nil || standard != nil {
		t.Fatal("standard template modified", err)
	}
	for _, path := range []string{s.Certificate, s.PrivateKey, s.Database, template} {
		if exportConfiguredApp(s, template, path) == nil {
			t.Fatal("protected path overwritten", path)
		}
	}
	s.Host = "wrong.example"
	if exportConfiguredApp(s, template, destination) == nil {
		t.Fatal("certificate hostname mismatch accepted")
	}
	p, err = appprofile.Read(destination)
	if err != nil || p.Server != "https://127.0.0.1:8443" {
		t.Fatal("failed export changed existing destination")
	}
}
