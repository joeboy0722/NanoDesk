package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

type serverSettings struct {
	PublicIP    string `json:"public_ip"`
	Host        string `json:"host"`
	ListenIP    string `json:"listen_ip"`
	HTTPSPort   int    `json:"https_port"`
	TURNPort    int    `json:"turn_port"`
	TURNTLSPort int    `json:"turn_tls_port"`
	RelayMin    int    `json:"relay_min"`
	RelayMax    int    `json:"relay_max"`
	RelayMbps   int    `json:"relay_mbps"`
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private_key"`
	CAFile      string `json:"ca_file,omitempty"`
	Database    string `json:"database"`
	AutoStart   bool   `json:"auto_start"`
}

func settingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		base = local
	}
	return filepath.Join(base, "NanoDesk-Server", "server-settings.json"), nil
}
func defaults(base string) serverSettings {
	exe, _ := os.Executable()
	exeDir := filepath.Dir(exe)
	db := filepath.Join(base, "nanodesk.db")
	// Preserve an existing database from the earlier CLI deployment.
	legacy := filepath.Join(exeDir, "nanodesk.db")
	if info, e := os.Stat(legacy); e == nil && info.Mode().IsRegular() {
		db = legacy
	}
	return serverSettings{ListenIP: "0.0.0.0", HTTPSPort: 443, TURNPort: 3478, TURNTLSPort: 5349, RelayMin: 49160, RelayMax: 49260, RelayMbps: 20, Certificate: filepath.Join(exeDir, "server.crt"), PrivateKey: filepath.Join(exeDir, "server.key"), Database: db}
}
func loadSettings(path string) (serverSettings, error) {
	s := defaults(filepath.Dir(path))
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("設定檔無法解析，請先保留備份：%w", err)
	}
	return s.resolved(filepath.Dir(path)), nil
}
func (s serverSettings) resolved(base string) serverSettings {
	for _, p := range []*string{&s.Certificate, &s.PrivateKey, &s.CAFile, &s.Database} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	s.PublicIP = strings.TrimSpace(s.PublicIP)
	s.Host = strings.TrimSpace(s.Host)
	s.ListenIP = strings.TrimSpace(s.ListenIP)
	return s
}
func (s serverSettings) validateEndpoint() error {
	if s.hostname() == "" {
		return fmt.Errorf("請填寫 IPv4 或網域（DDNS）")
	}
	if s.PublicIP != "" && net.ParseIP(s.PublicIP).To4() == nil {
		return fmt.Errorf("IPv4 欄位必須是有效的 IPv4 位址")
	}
	if s.Host != "" && (strings.ContainsAny(s.Host, " /\\:@?#\t\r\n") || len(s.Host) > 253) {
		return fmt.Errorf("網域請只填主機名稱，例如 desk.example.com，不含 https:// 或連接埠")
	}
	return nil
}

func (s serverSettings) validate() error {
	if err := s.validateEndpoint(); err != nil {
		return err
	}
	if net.ParseIP(s.ListenIP).To4() == nil {
		return fmt.Errorf("監聽 IP 必須是 IPv4；所有網卡請填 0.0.0.0")
	}
	for _, p := range []int{s.HTTPSPort, s.TURNPort, s.TURNTLSPort} {
		if p < 1 || p > 65535 {
			return fmt.Errorf("連接埠必須介於 1 至 65535")
		}
	}
	if s.HTTPSPort == s.TURNTLSPort {
		return fmt.Errorf("HTTPS 與 TURN TLS 不可使用相同 TCP 埠")
	}
	if s.RelayMin < 1024 || s.RelayMax >= 65535 || s.RelayMax < s.RelayMin {
		return fmt.Errorf("中繼 UDP 範圍需介於 1024 至 65534，且起始值不可大於結束值")
	}
	if s.TURNPort >= s.RelayMin && s.TURNPort <= s.RelayMax {
		return fmt.Errorf("TURN UDP 埠不可落在中繼配置範圍內")
	}
	if s.RelayMbps < 1 || s.RelayMbps > 1000 {
		return fmt.Errorf("中繼頻寬需介於 1 至 1000 Mbit/s")
	}
	if s.Certificate == "" || s.PrivateKey == "" {
		return fmt.Errorf("請選擇 TLS 憑證與私鑰，或按「建立自簽憑證」")
	}
	if s.Database == "" {
		return fmt.Errorf("請選擇 ID 資料庫位置")
	}
	return nil
}
func saveSettings(path string, s serverSettings) error {
	s = s.resolved(filepath.Dir(path))
	if err := s.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, b, 0600)
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".nanodesk-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}
func (s serverSettings) hostname() string {
	if s.Host != "" {
		return s.Host
	}
	return s.PublicIP
}
func (s serverSettings) appURL() string {
	if s.HTTPSPort == 443 {
		return "https://" + s.hostname()
	}
	return "https://" + net.JoinHostPort(s.hostname(), fmt.Sprint(s.HTTPSPort))
}
