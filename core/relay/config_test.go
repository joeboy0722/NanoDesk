package relay

import (
	"net/netip"
	"testing"
)

func TestConfigSourcesAndBindings(t *testing.T) {
	c := Config{TotalMbps: 100, SessionMbps: 5, Groups: []GroupConfig{
		{ID: "public", Name: "Public", Default: true, CapacityMbps: 100, Endpoints: []EndpointConfig{{ID: "wan", BindIP: "127.0.0.1", PublicHost: "relay.example.com", Ports: []uint16{3478, 3479}, Enabled: true}}},
		{ID: "private", Name: "Private", Sources: []string{"10.0.0.0/8", "fd00::/8"}, CapacityMbps: 20, Endpoints: []EndpointConfig{{ID: "lan", BindIP: "::1", PublicHost: "::1", Ports: []uint16{3478}, Enabled: true}}},
		{ID: "specific", Name: "Specific", Sources: []string{"10.1.0.0/16"}, CapacityMbps: 10, Endpoints: []EndpointConfig{{ID: "other", BindIP: "127.0.0.2", PublicHost: "127.0.0.2", Ports: []uint16{3478}, Enabled: true}}},
	}}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for address, want := range map[string]string{"10.1.2.3": "specific", "10.2.1.1": "private", "fd00::1": "private", "192.0.2.1": "public", "::ffff:10.1.2.3": "specific"} {
		if got := c.SelectGroup(netip.MustParseAddr(address)); got.ID != want {
			t.Fatalf("%s: %s != %s", address, got.ID, want)
		}
	}
	c.Groups[0].Endpoints[0].Ports = []uint16{3478, 3478}
	if err := c.Validate(); err == nil {
		t.Fatal("duplicate socket binding accepted")
	}
}
