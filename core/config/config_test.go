package config

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// 測試 UTF-8 BOM 設定檔相容性
func TestLoadConfigWithBOM(t *testing.T) {
	configPath = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { configPath = "" })
	cfgPath := GetConfigPath()
	origContent, origErr := os.ReadFile(cfgPath)
	defer func() {
		if origErr == nil {
			_ = os.WriteFile(cfgPath, origContent, 0644)
		} else {
			_ = os.Remove(cfgPath)
		}
	}()

	bomData := append([]byte("\xef\xbb\xbf"), []byte(`{"remember_passwords":true,"admin_password":"bom_pass!@#"}`)...)
	_ = os.WriteFile(cfgPath, bomData, 0644)

	cfg := LoadConfig()
	if cfg.AdminPassword != "bom_pass!@#" {
		t.Fatalf("UTF-8 BOM 設定檔載入失敗: 得到 %s", cfg.AdminPassword)
	}
}

// 測試設定檔之儲存與讀取持久化
func TestConfigSaveAndLoad(t *testing.T) {
	// 備份現存設定檔（若有）
	configPath = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { configPath = "" })
	cfgPath := GetConfigPath()
	origContent, origErr := os.ReadFile(cfgPath)
	defer func() {
		if origErr == nil {
			_ = os.WriteFile(cfgPath, origContent, 0644)
		} else {
			_ = os.Remove(cfgPath)
		}
	}()

	testCfg := &AppConfig{
		RememberPasswords: true,
		ViewPassword:      "View*Special@123",
		StandardPassword:  "Std&Control#456",
		AdminPassword:     "SuperAdmin!Secret$789",
		UploadLimitGiB:    10,
	}

	if err := SaveConfig(testCfg); err != nil {
		t.Fatalf("儲存設定檔失敗: %v", err)
	}

	raw, err := os.ReadFile(cfgPath)
	if err != nil || bytes.Contains(raw, []byte(testCfg.AdminPassword)) {
		t.Fatal("password stored in plaintext", err)
	}
	loaded := LoadConfig()
	if loaded.UploadLimitGiB != 10 {
		t.Fatalf("upload limit lost: %d", loaded.UploadLimitGiB)
	}
	if !loaded.RememberPasswords {
		t.Errorf("RememberPasswords 預期為 true，得到 false")
	}
	if loaded.ViewPassword != testCfg.ViewPassword {
		t.Errorf("ViewPassword 不符：預期 %s，得到 %s", testCfg.ViewPassword, loaded.ViewPassword)
	}
	if loaded.StandardPassword != testCfg.StandardPassword {
		t.Errorf("StandardPassword 不符：預期 %s，得到 %s", testCfg.StandardPassword, loaded.StandardPassword)
	}
	if loaded.AdminPassword != testCfg.AdminPassword {
		t.Errorf("AdminPassword 不符：預期 %s，得到 %s", testCfg.AdminPassword, loaded.AdminPassword)
	}
}

func TestLegacyMigrationAndDisabledRoles(t *testing.T) {
	configPath = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { configPath = "" })
	legacy := []byte(`{"remember_passwords":true,"admin_password":"legacy-secret"}`)
	if err := os.WriteFile(configPath, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := LoadConfig()
	if !cfg.Loaded || cfg.LoadError != nil {
		t.Fatal(cfg.LoadError)
	}
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(configPath)
	if bytes.Contains(raw, []byte("legacy-secret")) || !bytes.Contains(raw, []byte("protected_passwords")) {
		t.Fatal("legacy password not migrated")
	}
	cfg = LoadConfig()
	if !cfg.Loaded || cfg.LoadError != nil || cfg.ViewPassword != "" || cfg.StandardPassword != "" || cfg.AdminPassword != "legacy-secret" {
		t.Fatal("disabled roles lost", cfg.LoadError)
	}
}

func TestCorruptProtectedConfigFailsClosed(t *testing.T) {
	configPath = filepath.Join(t.TempDir(), "config.json")
	t.Cleanup(func() { configPath = "" })
	if err := os.WriteFile(configPath, []byte(`{"protected_passwords":"not-base64"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if LoadConfig().LoadError == nil {
		t.Fatal("corrupt config silently accepted")
	}
}
