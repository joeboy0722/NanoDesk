package engine

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"kvm/core/identity"
	"kvm/core/protocol"
)

func trustedTLS(s identity.Settings) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.CAFile != "" {
		b, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, err
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("CA 憑證無效")
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// Pion's proxy-dialer hook lets TURN TLS use the same explicit CA trust as
// signaling. It never bypasses certificate or hostname validation.
type turnTLSDialer struct {
	allowed map[string]bool
	config  *tls.Config
}

func (d *turnTLSDialer) Dial(_ string, address string) (net.Conn, error) {
	if !d.allowed[address] {
		return nil, fmt.Errorf("unconfigured TURN TLS endpoint")
	}
	return tls.DialWithDialer(&net.Dialer{Timeout: 8 * time.Second}, "tcp", address, d.config.Clone())
}
func peerAPI(settings identity.Settings, servers []protocol.ICE) (*webrtc.API, error) {
	cfg, err := trustedTLS(settings)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, server := range servers {
		for _, raw := range server.URLs {
			if strings.HasPrefix(raw, "turns:") {
				address := strings.SplitN(strings.TrimPrefix(raw, "turns:"), "?", 2)[0]
				allowed[address] = true
			}
		}
	}
	var se webrtc.SettingEngine
	se.SetICEProxyDialer(&turnTLSDialer{allowed: allowed, config: cfg})
	se.SetSCTPMaxMessageSize(65536)
	return webrtc.NewAPI(webrtc.WithSettingEngine(se)), nil
}
