package relay

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

type EndpointConfig struct {
	ID         string   `json:"id"`
	BindIP     string   `json:"bind_ip"`
	PublicHost string   `json:"public_host"`
	Ports      []uint16 `json:"ports"`
	Enabled    bool     `json:"enabled"`
}

type GroupConfig struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	Sources      []string         `json:"sources,omitempty"`
	Default      bool             `json:"default"`
	CapacityMbps int64            `json:"capacity_mbps"`
	Endpoints    []EndpointConfig `json:"endpoints"`
}

type Config struct {
	TotalMbps   int64         `json:"total_mbps"`
	SessionMbps int64         `json:"session_mbps"`
	Groups      []GroupConfig `json:"groups"`
}

func (c Config) Validate() error {
	if c.TotalMbps <= 0 || c.SessionMbps <= 0 || c.SessionMbps > c.TotalMbps || len(c.Groups) == 0 {
		return fmt.Errorf("invalid relay capacity")
	}
	ids := make(map[string]bool)
	bindings := make(map[netip.AddrPort]bool)
	sources := make(map[netip.Prefix]bool)
	defaults := 0
	for _, g := range c.Groups {
		if g.ID == "" || ids[g.ID] || strings.TrimSpace(g.Name) == "" || g.CapacityMbps < c.SessionMbps {
			return fmt.Errorf("invalid relay group %q", g.ID)
		}
		ids[g.ID] = true
		if g.Default {
			defaults++
		}
		for _, raw := range g.Sources {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || prefix.Addr().Is4In6() {
				return fmt.Errorf("invalid relay source %q", raw)
			}
			prefix = prefix.Masked()
			if sources[prefix] {
				return fmt.Errorf("duplicate relay source %q", raw)
			}
			sources[prefix] = true
		}
		for _, endpoint := range g.Endpoints {
			if endpoint.ID == "" || ids[endpoint.ID] {
				return fmt.Errorf("duplicate or empty endpoint ID")
			}
			ids[endpoint.ID] = true
			ip, err := netip.ParseAddr(endpoint.BindIP)
			if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Is4In6() {
				return fmt.Errorf("endpoint %s requires a specific unicast bind IP", endpoint.ID)
			}
			if !validPublicHost(endpoint.PublicHost) || len(endpoint.Ports) == 0 {
				return fmt.Errorf("invalid endpoint %s address or ports", endpoint.ID)
			}
			for _, port := range endpoint.Ports {
				binding := netip.AddrPortFrom(ip, port)
				if port == 0 || bindings[binding] {
					return fmt.Errorf("duplicate or invalid relay binding %s", binding)
				}
				bindings[binding] = true
			}
		}
	}
	if defaults != 1 {
		return fmt.Errorf("relay requires exactly one default group")
	}
	return nil
}

func validPublicHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	if len(host) == 0 || len(host) > 253 || strings.ContainsAny(host, " /\\:@?#\t\r\n") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return net.ParseIP(host) == nil
}

// SelectGroup applies longest-prefix matching to a validated configuration.
func (c Config) SelectGroup(source netip.Addr) GroupConfig {
	source = source.Unmap()
	var selected, fallback GroupConfig
	best := -1
	for _, g := range c.Groups {
		if g.Default {
			fallback = g
		}
		for _, raw := range g.Sources {
			prefix, err := netip.ParsePrefix(raw)
			if err == nil && prefix.Contains(source) && prefix.Bits() > best {
				selected, best = g, prefix.Bits()
			}
		}
	}
	if best >= 0 {
		return selected
	}
	return fallback
}
