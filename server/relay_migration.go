package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"kvm/core/relay"
)

// Migration proposes a concrete bind address but requires an explicit save
// before starting. The original bytes are preserved even if later steps fail.
func migrateRelaySettings(path string, original []byte, s serverSettings) (serverSettings, error) {
	backup := path + ".pre-relay-v2.bak"
	file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		existing, readErr := os.ReadFile(backup)
		if readErr != nil || !bytes.Equal(existing, original) {
			return s, fmt.Errorf("設定備份已存在且內容不同: %s", backup)
		}
	} else if err != nil {
		return s, err
	} else {
		_, writeErr := file.Write(original)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		closeErr := file.Close()
		if writeErr != nil {
			return s, writeErr
		}
		if closeErr != nil {
			return s, closeErr
		}
	}
	s.SchemaVersion = 2
	s.MigrationPending = true
	s.AutoStart = false
	if s.RelayPool == nil {
		bind := net.ParseIP(s.ListenIP)
		if bind == nil || bind.IsUnspecified() {
			bind = net.ParseIP("127.0.0.1")
			if interfaces, err := net.Interfaces(); err == nil {
				found := false
				for _, iface := range interfaces {
					if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
						continue
					}
					addresses, _ := iface.Addrs()
					for _, address := range addresses {
						ip, _, err := net.ParseCIDR(address.String())
						if err == nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() {
							bind = ip
							found = true
							break
						}
					}
					if found {
						break
					}
				}
			}
		}
		capacity := int64(s.RelayMbps)
		if capacity <= 0 {
			capacity = 20
		}
		host := s.hostname()
		if host == "" {
			host = bind.String()
		}
		port := s.TURNPort
		if port < 1 || port > 65535 {
			port = 3478
		}
		s.RelayPool = &relay.Config{TotalMbps: capacity, SessionMbps: min(5, capacity), Groups: []relay.GroupConfig{{
			ID: "default", Name: "預設中繼", Default: true, CapacityMbps: capacity,
			Endpoints: []relay.EndpointConfig{{ID: "primary", BindIP: bind.String(), PublicHost: host, Ports: []uint16{uint16(port)}, Enabled: true}},
		}}}
	}
	s = s.resolved(filepath.Dir(path))
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return s, err
	}
	if err := atomicWrite(path, data, 0600); err != nil {
		return s, err
	}
	return s, nil
}
