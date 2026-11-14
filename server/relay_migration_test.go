package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRelaySettingsMigrationKeepsOriginalAndRequiresConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"public_ip":"127.0.0.1","listen_ip":"127.0.0.1","turn_port":3478,"relay_mbps":20,"auto_start":true}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	settings, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".pre-relay-v2.bak")
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("original settings not preserved", err)
	}
	if !settings.MigrationPending || settings.AutoStart || settings.SchemaVersion != 2 || settings.RelayPool == nil {
		t.Fatal("migration did not require review")
	}
	if err := settings.RelayPool.Validate(); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSettings(path)
	if err != nil || !loaded.MigrationPending {
		t.Fatal("review requirement lost on reload", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var content map[string]any
	if err := json.Unmarshal(before, &content); err != nil {
		t.Fatal(err)
	}
	if content["schema_version"] != float64(2) {
		t.Fatal("new settings not persisted")
	}
	if _, err := startServer(settings); err == nil {
		t.Fatal("unreviewed migration started service")
	}
}
