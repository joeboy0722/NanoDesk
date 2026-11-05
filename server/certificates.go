package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func exportPublicCA(source, destination string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 2*1024*1024 {
		return fmt.Errorf("CA 檔案不是一般檔案或超過 2 MiB")
	}
	b, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	var out []byte
	for len(bytes.TrimSpace(b)) > 0 {
		block, rest := pem.Decode(b)
		if block == nil || block.Type != "CERTIFICATE" {
			return fmt.Errorf("CA 匯出只接受公開 PEM 憑證，不接受私鑰或其他資料")
		}
		if _, err = x509.ParseCertificate(block.Bytes); err != nil {
			return err
		}
		out = append(out, pem.EncodeToMemory(block)...)
		b = rest
	}
	if len(out) == 0 {
		return fmt.Errorf("CA 檔案沒有公開憑證")
	}
	return atomicWrite(destination, out, 0600)
}

// The CA signing key is discarded; only the server's TLS private key is saved.
func createCertificates(base string, s serverSettings) (serverSettings, error) {
	if err := s.validateEndpoint(); err != nil {
		return s, err
	}
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return s, err
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return s, err
	}
	serial := func() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }
	rootSerial, err := serial()
	if err != nil {
		return s, err
	}
	leafSerial, err := serial()
	if err != nil {
		return s, err
	}
	now := time.Now()
	root := &x509.Certificate{SerialNumber: rootSerial, Subject: pkix.Name{CommonName: "NanoDesk Local CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(3, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		return s, err
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		return s, err
	}
	leaf := &x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: s.hostname()}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(s.PublicIP); ip != nil {
		leaf.IPAddresses = append(leaf.IPAddresses, ip)
	}
	if ip := net.ParseIP(s.Host); ip != nil {
		leaf.IPAddresses = append(leaf.IPAddresses, ip)
	} else if s.Host != "" {
		leaf.DNSNames = []string{s.Host}
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		return s, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		return s, err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return s, err
	}
	dir, err := os.MkdirTemp(base, "certificate-"+now.Format("20060102-150405")+"-")
	if err != nil {
		return s, err
	}
	out := s
	out.Certificate = filepath.Join(dir, "server.crt")
	out.PrivateKey = filepath.Join(dir, "server.key")
	out.CAFile = filepath.Join(dir, "server-ca.crt")
	for _, f := range []struct {
		path string
		data []byte
	}{{out.Certificate, append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})...)}, {out.PrivateKey, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})}, {out.CAFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})}} {
		if err = os.WriteFile(f.path, f.data, 0600); err != nil {
			os.Remove(out.Certificate)
			os.Remove(out.PrivateKey)
			os.Remove(out.CAFile)
			os.Remove(dir)
			return s, err
		}
	}
	return out, nil
}
