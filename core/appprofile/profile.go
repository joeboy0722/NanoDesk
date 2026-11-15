// Package appprofile contains the public connection profile embedded in an APP.
package appprofile

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

const Marker = "NanoDesk.App.Template/1"
const maxProfile = 1024 * 1024

type Profile struct {
	Version int    `json:"version"`
	Server  string `json:"server"`
	CAPEM   string `json:"ca_pem"`
}

func (p Profile) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("不支援的 APP 內嵌設定版本：%d", p.Version)
	}
	u, err := url.Parse(p.Server)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(p.Server, "\r\n") {
		return fmt.Errorf("內嵌網址必須是 https://主機:連接埠")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return fmt.Errorf("內嵌 HTTPS 埠無效")
		}
	}
	if len(strings.TrimSpace(p.CAPEM)) == 0 || len(p.CAPEM)+len(p.Server) > maxProfile/2 {
		return fmt.Errorf("內嵌 CA 為空或過大")
	}
	b := []byte(p.CAPEM)
	for len(bytes.TrimSpace(b)) > 0 {
		b = bytes.TrimSpace(b)
		if !bytes.HasPrefix(b, []byte("-----BEGIN CERTIFICATE-----")) {
			return fmt.Errorf("內嵌 CA 只接受公開 PEM 憑證")
		}
		block, rest := pem.Decode(b)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			return fmt.Errorf("內嵌 CA 格式無效")
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("內嵌 CA 無效：%w", err)
		}
		b = rest
	}
	return nil
}

func decode(b []byte) (*Profile, error) {
	if len(b) > maxProfile {
		return nil, fmt.Errorf("APP 內嵌設定過大")
	}
	var p Profile
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("APP 內嵌設定損壞：%w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("APP 內嵌設定包含多餘資料")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
