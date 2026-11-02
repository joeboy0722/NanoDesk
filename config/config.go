package config

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AppConfig 應用程式全域組態設定結構
type AppConfig struct {
	Loaded            bool   `json:"-"`
	LoadError         error  `json:"-"`
	UploadLimitGiB    int64  `json:"upload_limit_gib,omitempty"`
	RememberPasswords bool   `json:"remember_passwords"` // 是否保留自訂密碼 (預設 true)
	ViewPassword      string `json:"view_password"`      // 僅觀看自訂密碼
	StandardPassword  string `json:"standard_password"`  // 一般控制自訂密碼
	AdminPassword     string `json:"admin_password"`     // 完全控制自訂密碼
}

var (
	configMu   sync.RWMutex
	cachedCfg  *AppConfig
	configPath string
)

// GetConfigPath 取得 config.json 的絕對路徑 (與當前執行檔同目錄)
func GetConfigPath() string {
	if configPath != "" {
		return configPath
	}
	exePath, err := os.Executable()
	if err != nil {
		return "config.json"
	}
	return filepath.Join(filepath.Dir(exePath), "config.json")
}

// LoadConfig 載入本地設定檔，若不存在則回傳預設設定 (相容 UTF-8 BOM)
func LoadConfig() *AppConfig {
	configMu.Lock()
	defer configMu.Unlock()

	cfgPath := GetConfigPath()
	data, err := os.ReadFile(cfgPath)

	if err != nil {
		if !os.IsNotExist(err) {
			return &AppConfig{LoadError: err}
		}
		// 設定檔不存在或無法讀取，使用預設值
		cachedCfg = &AppConfig{
			RememberPasswords: true,
		}
		return cachedCfg
	}

	// 移除 Windows 常見的 UTF-8 BOM 標頭，防止 json.Unmarshal 錯誤
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))

	var envelope struct {
		Protected string `json:"protected_passwords"`
	}
	var cfg AppConfig
	err = json.Unmarshal(data, &envelope)
	if err == nil && envelope.Protected != "" {
		var encoded []byte
		encoded, err = base64.StdEncoding.DecodeString(envelope.Protected)
		if err == nil {
			data, err = protect(encoded, true)
		}
	}
	if err == nil {
		err = json.Unmarshal(data, &cfg)
	}
	if err != nil {
		cachedCfg = &AppConfig{RememberPasswords: true, LoadError: fmt.Errorf("設定檔無法讀取: %w", err)}
		return cachedCfg
	}

	cfg.Loaded = true
	cachedCfg = &cfg
	return cachedCfg
}

// SaveConfig 儲存設定至本地 config.json
func SaveConfig(cfg *AppConfig) error {
	configMu.Lock()
	defer configMu.Unlock()

	cfgPath := GetConfigPath()
	copyCfg := *cfg
	copyCfg.LoadError = nil
	if !copyCfg.RememberPasswords {
		copyCfg.ViewPassword = ""
		copyCfg.StandardPassword = ""
		copyCfg.AdminPassword = ""
	}
	data, err := json.Marshal(&copyCfg)
	if err != nil {
		return err
	}
	encrypted, err := protect(data, false)
	if err != nil {
		return err
	}
	data, err = json.Marshal(struct {
		Protected string `json:"protected_passwords"`
	}{base64.StdEncoding.EncodeToString(encrypted)})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".nanodesk-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), cfgPath); err != nil {
		return err
	}
	cachedCfg = &copyCfg
	return nil
}
