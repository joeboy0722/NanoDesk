package main

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
)

func TestServerSettingsRoundTripAndValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.AutoStart {
		t.Fatal("unexpected auto start")
	}
	s.PublicIP = "127.0.0.1"
	s.Host = "desk.example.test"
	s.Certificate = "server.crt"
	s.PrivateKey = "server.key"
	s.Database = "ids.db"
	s.AutoStart = true
	if err = saveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AutoStart || loaded.PublicIP != s.PublicIP || loaded.Database != filepath.Join(filepath.Dir(path), "ids.db") {
		t.Fatal(loaded)
	}
	cases := map[string]func(*serverSettings){"invalid IP": func(s *serverSettings) { s.PublicIP = "bad" }, "URL in host": func(s *serverSettings) { s.Host = "https://host" }, "TCP collision": func(s *serverSettings) { s.TURNTLSPort = s.HTTPSPort }, "port range": func(s *serverSettings) { s.HTTPSPort = 65536 }, "relay order": func(s *serverSettings) { s.RelayMax = s.RelayMin - 1 }, "TURN in relay range": func(s *serverSettings) { s.TURNPort = s.RelayMin }, "bandwidth": func(s *serverSettings) { s.RelayMbps = 0 }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			invalid := loaded
			change(&invalid)
			if err := saveSettings(path, invalid); err == nil {
				t.Fatal("invalid settings accepted")
			}
			still, err := loadSettings(path)
			if err != nil || still != loaded {
				t.Fatal("failed save changed stored settings", err)
			}
		})
	}
	if err = os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = loadSettings(path); err == nil {
		t.Fatal("corrupt configuration silently replaced")
	}
}
func TestGeneratedCertificatesAndTrust(t *testing.T) {
	s := defaults(t.TempDir())
	s.PublicIP = "127.0.0.1"
	s.Host = "desk.example.test"
	generated, err := createCertificates(t.TempDir(), s)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(generated.Certificate, generated.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(generated.CAFile)
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		t.Fatal("CA unavailable", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"127.0.0.1", "desk.example.test"} {
		if _, err = leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots}); err != nil {
			t.Fatal(name, err)
		}
	}
	if _, err = leaf.Verify(x509.VerifyOptions{DNSName: "wrong.example.test", Roots: roots}); err == nil {
		t.Fatal("wrong hostname accepted")
	}
	files, err := os.ReadDir(filepath.Dir(generated.Certificate))
	if err != nil || len(files) != 3 {
		t.Fatal("unexpected certificate artifacts", files, err)
	}
	exported := filepath.Join(t.TempDir(), "exported-ca.crt")
	if err = exportPublicCA(generated.CAFile, exported); err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(exported)
	if err != nil || string(public) != string(ca) {
		t.Fatal("export changed public CA", err)
	}
	if err = exportPublicCA(generated.PrivateKey, exported); err == nil {
		t.Fatal("private key exported as a public CA")
	}
	unchanged, err := os.ReadFile(exported)
	if err != nil || string(unchanged) != string(ca) {
		t.Fatal("rejected export damaged destination", err)
	}
	again, err := createCertificates(filepath.Dir(filepath.Dir(generated.Certificate)), s)
	if err != nil {
		t.Fatal(err)
	}
	if again.Certificate == generated.Certificate {
		t.Fatal("existing certificate overwritten")
	}
}
