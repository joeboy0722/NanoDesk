package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"kvm/core/appprofile"
)

func exportConfiguredApp(s serverSettings, template, destination string, protected ...string) error {
	for _, path := range append(protected, s.Certificate, s.PrivateKey, s.CAFile, s.Database) {
		if path != "" && appprofile.SameFile(path, destination) {
			return fmt.Errorf("匯出不可覆寫伺服器、憑證、私鑰或資料庫")
		}
	}
	ca := s.CAFile
	if ca == "" {
		ca = s.Certificate
	}
	info, err := os.Stat(ca)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 512*1024 {
		return fmt.Errorf("公開 CA 檔案無效或過大")
	}
	b, err := os.ReadFile(ca)
	if err != nil {
		return err
	}
	p := appprofile.Profile{Version: 1, Server: s.appURL(), CAPEM: string(b)}
	if err = p.Validate(); err != nil {
		return err
	}
	// Verify the exported trust against the configured public server certificate.
	certPEM, err := os.ReadFile(s.Certificate)
	if err != nil {
		return err
	}
	leafBlock, remaining := pem.Decode(certPEM)
	if leafBlock == nil || leafBlock.Type != "CERTIFICATE" {
		return fmt.Errorf("伺服器 TLS 憑證格式無效")
	}
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	roots.AppendCertsFromPEM(b)
	intermediates := x509.NewCertPool()
	intermediates.AppendCertsFromPEM(remaining)
	if _, err = leaf.Verify(x509.VerifyOptions{DNSName: s.hostname(), Roots: roots, Intermediates: intermediates}); err != nil {
		return fmt.Errorf("匯出網址、CA 與伺服器憑證不符：%w", err)
	}
	return appprofile.Export(template, destination, p)
}

func (g *serverGUI) exportAppAction() {
	if g.busy || g.closing {
		return
	}
	if g.dirty {
		showMessage(g.hwnd, "請先儲存伺服器設定，再匯出 APP。", 0x40)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		showMessage(g.hwnd, err.Error(), 0x10)
		return
	}
	template := filepath.Join(filepath.Dir(executable), "NanoDesk-App.exe")
	destination := chooseFile(g.hwnd, "匯出預設連線 APP", filepath.Join(filepath.Dir(executable), "NanoDesk-App-Configured.exe"), "應用程式\x00*.exe\x00\x00", true, true)
	if destination == "" {
		return
	}
	s := g.saved.resolved(filepath.Dir(g.path))
	g.busy = true
	g.refresh()
	go func() {
		err := exportConfiguredApp(s, template, destination, executable, g.path)
		g.post(guiResult{kind: "app-export", text: destination, err: err})
	}()
}
