// Package transport contains the Web host's HTTP and WebSocket adapters.
package transport

import (
	"io/fs"
	"kvm/core/auth"
	"kvm/core/desktop"
	"net/http"
)

type Server struct {
	hub     *desktop.StreamHub
	authMgr *auth.AuthManager
	wsSlots chan struct{}
}

func New(hub *desktop.StreamHub, mgr *auth.AuthManager) *Server {
	return &Server{hub: hub, authMgr: mgr, wsSlots: make(chan struct{}, 64)}
}
func (s *Server) Handler(assets fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(assets)))
	mux.HandleFunc("/api/auth/login", s.handleLogin)
	mux.HandleFunc("/api/auth/request", s.handleAuthRequest)
	mux.HandleFunc("/api/auth/status", s.handleAuthStatus)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/ws", s.handleWebSocket)
	return secureHandler(mux)
}
