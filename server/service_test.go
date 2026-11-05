package main

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/turn/v5"
	"kvm/core/protocol"
)

func serviceSettings(t *testing.T) serverSettings {
	t.Helper()
	s := defaults(t.TempDir())
	s.PublicIP = "127.0.0.1"
	s.ListenIP = "127.0.0.1"
	a, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	u, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	s.HTTPSPort = a.Addr().(*net.TCPAddr).Port
	s.TURNTLSPort = b.Addr().(*net.TCPAddr).Port
	s.TURNPort = u.LocalAddr().(*net.UDPAddr).Port
	s.RelayMin = 45000
	s.RelayMax = 45100
	if s.TURNPort >= 45000 && s.TURNPort <= 45100 {
		s.RelayMin = 46000
		s.RelayMax = 46100
	}
	s, err = createCertificates(t.TempDir(), s)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func serverRoots(t *testing.T, s serverSettings) *x509.CertPool {
	t.Helper()
	roots := x509.NewCertPool()
	b, err := os.ReadFile(s.CAFile)
	if err != nil || !roots.AppendCertsFromPEM(b) {
		t.Fatal(err)
	}
	return roots
}
func TestServerStartStopRestartAndLiveBackup(t *testing.T) {
	s := serviceSettings(t)
	service, err := startServer(s)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: serverRoots(t, s)}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get(s.appURL() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "ok\n" {
		t.Fatal(string(body))
	}
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: serverRoots(t, s)}}
	ws, _, err := dialer.Dial(fmt.Sprintf("wss://127.0.0.1:%d/signal", s.HTTPSPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var m protocol.Signal
	if err = ws.ReadJSON(&m); err != nil {
		t.Fatal(err)
	}
	private := key(t)
	public := private.Public().(ed25519.PublicKey)
	ws.WriteJSON(protocol.Signal{Type: "register", Version: 1, Public: public, Signature: ed25519.Sign(private, protocol.Proof(m.Nonce))})
	if err = ws.ReadJSON(&m); err != nil || m.Type != "registered" {
		t.Fatal(m, err)
	}
	id := m.ID
	pending, _, err := dialer.Dial(fmt.Sprintf("wss://127.0.0.1:%d/signal", s.HTTPSPort), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pending.Close()
	if err = pending.ReadJSON(&m); err != nil {
		t.Fatal(err)
	}
	idleTLS, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.TURNTLSPort)), &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: serverRoots(t, s)})
	if err != nil {
		t.Fatal(err)
	}
	defer idleTLS.Close()
	halfOpen, err := net.DialTimeout("tcp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.TURNTLSPort)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer halfOpen.Close()
	relay, relayAddr, err := service.generator.AllocatePacketConn(turn.AllocateListenerConfig{Network: "udp4", UserID: "shutdown-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err = service.Backup(backup); err != nil {
		t.Fatal(err)
	}
	if err = service.Backup(s.Database); err == nil {
		t.Fatal("backup overwrote active DB")
	}
	copied, err := openRegistry(backup)
	if err != nil {
		t.Fatal(err)
	}
	copyID, err := copied.identify(public)
	copied.db.Close()
	if err != nil || copyID != id {
		t.Fatal("backup lost ID", copyID, id, err)
	}
	began := time.Now()
	service.Close()
	if time.Since(began) > 3*time.Second {
		t.Fatal("shutdown waited for handshake timeout")
	}
	if _, _, err = ws.ReadMessage(); err == nil {
		t.Fatal("registered websocket survived stop")
	}
	pending.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err = pending.ReadMessage(); err == nil {
		t.Fatal("pending websocket survived stop")
	}
	for _, conn := range []net.Conn{idleTLS, halfOpen} {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := conn.Read(make([]byte, 1)); err == nil {
			t.Fatal("TURN TLS connection survived stop")
		} else if e, ok := err.(net.Error); ok && e.Timeout() {
			t.Fatal("TURN TLS connection timed out instead of closing")
		}
	}
	released, err := net.ListenPacket("udp4", relayAddr.String())
	if err != nil {
		t.Fatal("relay allocation was not released", err)
	}
	released.Close()
	if len(service.generator.slots) != 0 {
		t.Fatal("relay allocation quota not released")
	}
	restarted, err := startServer(s)
	if err != nil {
		t.Fatal("ports or DB remained locked", err)
	}
	again, err := restarted.registry.identify(public)
	restarted.Close()
	if err != nil || again != id {
		t.Fatal("restart changed ID", err)
	}
	stoppedBackup := filepath.Join(t.TempDir(), "offline.db")
	if err = backupStoppedDatabase(s.Database, stoppedBackup); err != nil {
		t.Fatal(err)
	}
}
func TestFailedStartReleasesPortsAndDatabase(t *testing.T) {
	s := serviceSettings(t)
	occupied, err := net.Listen("tcp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.HTTPSPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	if service, err := startServer(s); err == nil {
		service.Close()
		t.Fatal("started with occupied HTTPS port")
	}
	u, err := net.ListenPacket("udp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.TURNPort)))
	if err != nil {
		t.Fatal("failed start leaked UDP port", err)
	}
	u.Close()
	l, err := net.Listen("tcp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.TURNTLSPort)))
	if err != nil {
		t.Fatal("failed start leaked TLS port", err)
	}
	l.Close()
	r, err := openRegistry(s.Database)
	if err != nil {
		t.Fatal("failed start leaked DB lock", err)
	}
	r.db.Close()
}
func TestControllerStopDuringStartup(t *testing.T) {
	c := &serverController{}
	s := serviceSettings(t)
	if err := c.Start(s); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(s); err == nil {
		t.Fatal("duplicate start accepted")
	}
	c.Stop()
	deadline := time.Now().Add(5 * time.Second)
	for c.Snapshot().Phase != "stopped" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if state := c.Snapshot(); state.Phase != "stopped" {
		t.Fatal(state)
	}
	service, err := startServer(s)
	if err != nil {
		t.Fatal("controller did not release resources", err)
	}
	service.Close()
}

func TestServerWithHostnameOnly(t *testing.T) {
	s := serviceSettings(t)
	s.PublicIP, s.Host = "", "localhost"
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := saveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	s, err = createCertificates(t.TempDir(), s)
	if err != nil {
		t.Fatal(err)
	}
	server, err := startServer(s)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if !server.generator.RelayAddress.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("wrong relay address: %v", server.generator.RelayAddress)
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: serverRoots(t, s)}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get(s.appURL() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health endpoint returned %s", response.Status)
	}
}
