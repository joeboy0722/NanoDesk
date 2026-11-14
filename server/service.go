package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	bolt "go.etcd.io/bbolt"
	"kvm/core/relay"
)

type runningServer struct {
	settings   serverSettings
	registry   *registry
	hub        *signalHub
	turn       *turn.Server
	http       *http.Server
	listener   net.Listener
	generator  *guardedRelay
	done       chan error
	operations sync.Mutex
	stopped    bool
}

func startServer(s serverSettings) (running *runningServer, err error) {
	if s.MigrationPending {
		return nil, fmt.Errorf("請先確認中繼端點池與容量並儲存設定")
	}
	if err = s.validate(); err != nil {
		return nil, err
	}
	relayIP := net.ParseIP(s.PublicIP).To4()
	if relayIP == nil && s.RelayPool == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		addresses, lookupErr := net.DefaultResolver.LookupIP(ctx, "ip4", s.Host)
		cancel()
		if lookupErr != nil {
			return nil, fmt.Errorf("無法解析網域 %s 的 IPv4：%w", s.Host, lookupErr)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("網域 %s 沒有 IPv4 位址（A 記錄）", s.Host)
		}
		relayIP = addresses[0].To4()
	}
	pair, err := tls.LoadX509KeyPair(s.Certificate, s.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("TLS 憑證／私鑰無法載入：%w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	if err = leaf.VerifyHostname(s.hostname()); err != nil {
		return nil, fmt.Errorf("憑證與網域／公網 IP 不符：%w", err)
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("TLS 憑證尚未生效或已過期")
	}
	if err = os.MkdirAll(filepath.Dir(s.Database), 0700); err != nil {
		return nil, err
	}
	r, err := openRegistry(s.Database)
	if err != nil {
		return nil, fmt.Errorf("ID 資料庫無法開啟：%w", err)
	}
	cleanup := []func(){func() { r.db.Close() }}
	defer func() {
		if err != nil {
			for i := len(cleanup) - 1; i >= 0; i-- {
				cleanup[i]()
			}
		}
	}()
	h := newHub(r, s.hostname(), s.TURNPort, s.TURNTLSPort)
	if s.RelayPool != nil {
		pool := relay.NewPool()
		pool.SetBindingPermission(h.allowBinding)
		if err = pool.Apply(*s.RelayPool); err != nil {
			pool.Close()
			return nil, err
		}
		h.relayPool = pool
		cleanup = append(cleanup, pool.Close)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	var generator *guardedRelay
	var ts *turn.Server
	if s.RelayPool == nil {
		generator = &guardedRelay{RelayAddressGeneratorPortRange: &turn.RelayAddressGeneratorPortRange{RelayAddress: relayIP, Address: s.ListenIP, MinPort: uint16(s.RelayMin), MaxPort: uint16(s.RelayMax)}, hub: h, slots: make(chan struct{}, 256), bytesPerSec: s.RelayMbps * 1000000 / 8}
		cleanup = append(cleanup, generator.Close)
		udp, err := net.ListenPacket("udp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.TURNPort)))
		if err != nil {
			return nil, fmt.Errorf("TURN UDP 埠無法監聽：%w", err)
		}
		cleanup = append(cleanup, func() { udp.Close() })
		rawListener, err := net.Listen("tcp4", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.TURNTLSPort)))
		if err != nil {
			return nil, fmt.Errorf("TURN TLS 埠無法監聽：%w", err)
		}
		tlsListener := tls.NewListener(&trackedListener{Listener: rawListener}, cfg.Clone())
		cleanup = append(cleanup, func() { tlsListener.Close() })
		permit := func(a net.Addr, ip net.IP) bool {
			if ip.Equal(relayIP) {
				source, _, _ := net.SplitHostPort(a.String())
				h.mu.Lock()
				defer h.mu.Unlock()
				return h.hasSource(source)
			}
			return h.permit(a, ip)
		}
		ts, err = turn.NewServer(turn.ServerConfig{Realm: "nanodesk", LoggerFactory: &logging.DefaultLoggerFactory{Writer: log.Writer(), DefaultLogLevel: logging.LogLevelWarn}, AuthHandler: func(a *turn.RequestAttributes) (string, []byte, bool) {
			if !h.validTURN(a.Username, a.SrcAddr) {
				return "", nil, false
			}
			return a.Username, turn.GenerateAuthKey(a.Username, "nanodesk", h.password(a.Username)), true
		}, AllocationLifetime: time.Minute, PermissionTimeout: time.Minute, PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: udp, RelayAddressGenerator: generator, PermissionHandler: permit}}, ListenerConfigs: []turn.ListenerConfig{{Listener: tlsListener, RelayAddressGenerator: generator, PermissionHandler: permit}}})
		if err != nil {
			return nil, err
		}
		cleanup = append(cleanup, func() { ts.Close() })
	}

	ln, err := tls.Listen("tcp", net.JoinHostPort(s.ListenIP, fmt.Sprint(s.HTTPSPort)), cfg.Clone())
	if err != nil {
		return nil, fmt.Errorf("HTTPS 埠無法監聽：%w", err)
	}
	cleanup = append(cleanup, func() { ln.Close() })
	mux := http.NewServeMux()
	mux.HandleFunc("/signal", h.serve)
	mux.HandleFunc("/relay", h.serveRelay)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	running = &runningServer{settings: s, registry: r, hub: h, turn: ts, http: srv, listener: ln, generator: generator, done: make(chan error, 1)}
	done := running.done
	go func() {
		e := srv.Serve(ln)
		if e == http.ErrServerClosed {
			e = nil
		}
		done <- e
		close(done)
	}()
	if h.relayPool != nil {
		log.Printf("已啟動：%s；共用埠中繼 %d 個 socket；TLS 中繼共用 HTTPS", s.appURL(), len(h.relayPool.Stats()))
	} else {
		log.Printf("已啟動：%s；TURN UDP %d／TLS %d；中繼 IPv4 %s，UDP %d–%d", s.appURL(), s.TURNPort, s.TURNTLSPort, relayIP, s.RelayMin, s.RelayMax)
	}
	return running, nil
}
func (s *runningServer) Close() {
	s.operations.Lock()
	defer s.operations.Unlock()
	if s.stopped {
		return
	}
	s.stopped = true
	s.http.Close()
	s.listener.Close()
	s.hub.close()
	if s.turn != nil {
		s.turn.Close()
	}
	if s.generator != nil {
		s.generator.Close()
	}
	if s.hub.relayPool != nil {
		s.hub.relayPool.Close()
	}
	s.registry.db.Close()
	log.Print("伺服器已停止，所有連線已關閉")
}

// ApplyRelaySettings is serialized with shutdown and database operations.
func (s *runningServer) ApplyRelaySettings(path string, next serverSettings) error {
	s.operations.Lock()
	defer s.operations.Unlock()
	if s.stopped {
		return fmt.Errorf("服務已停止")
	}
	next = next.resolved(filepath.Dir(path))
	if err := next.validate(); err != nil {
		return err
	}
	oldStatic, newStatic := s.settings, next
	oldStatic.RelayPool, newStatic.RelayPool = nil, nil
	if !reflect.DeepEqual(oldStatic, newStatic) {
		return fmt.Errorf("運作中僅可變更中繼端點與容量，其他設定請先停止服務")
	}
	if s.hub.relayPool == nil || next.RelayPool == nil {
		return fmt.Errorf("首次啟用或移除中繼池請先停止服務")
	}
	if err := s.hub.relayPool.ApplyWithCommit(*next.RelayPool, func() error { return saveSettings(path, next) }); err != nil {
		return err
	}
	s.settings = next
	return nil
}

func (s *runningServer) Backup(destination string) error {
	s.operations.Lock()
	defer s.operations.Unlock()
	if s.stopped {
		return fmt.Errorf("伺服器已停止，請重新執行備份")
	}
	return backupDatabase(s.registry.db, destination)
}
func backupDatabase(db *bolt.DB, destination string) error {
	src, err := filepath.Abs(db.Path())
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if samePath(src, dst) {
		return fmt.Errorf("備份目的地不可與正在使用的 ID 資料庫相同")
	}
	if a, e := os.Stat(src); e == nil {
		if b, e := os.Stat(dst); e == nil && os.SameFile(a, b) {
			return fmt.Errorf("備份目的地與 ID 資料庫是同一個檔案")
		}
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".nanodesk-backup-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	err = db.View(func(tx *bolt.Tx) error { _, e := tx.WriteTo(f); return e })
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
	return os.Rename(temp, dst)
}
func backupStoppedDatabase(source, destination string) error {
	db, err := bolt.Open(source, 0600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	return backupDatabase(db, destination)
}

func (s *runningServer) DisableRelayEndpoint(path, id string) (serverSettings, error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	if s.stopped || s.hub.relayPool == nil {
		return serverSettings{}, fmt.Errorf("中繼服務未啟動")
	}
	var next serverSettings
	data, err := json.Marshal(s.settings)
	if err != nil {
		return next, err
	}
	if err = json.Unmarshal(data, &next); err != nil {
		return next, err
	}
	found := false
	for gi := range next.RelayPool.Groups {
		for ei := range next.RelayPool.Groups[gi].Endpoints {
			endpoint := &next.RelayPool.Groups[gi].Endpoints[ei]
			if endpoint.ID == id {
				endpoint.Enabled = false
				found = true
			}
		}
	}
	if !found {
		return next, fmt.Errorf("中繼端點不存在")
	}
	if err := s.hub.relayPool.ApplyWithCommit(*next.RelayPool, func() error { return saveSettings(path, next) }); err != nil {
		return next, err
	}
	s.settings = next
	s.hub.interruptRelayEndpoint(id)
	return next, nil
}
