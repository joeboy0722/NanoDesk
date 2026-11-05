package transport

import (
	"encoding/json"
	"fmt"
	"kvm/core/auth"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func requestIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && u.Scheme == scheme && strings.EqualFold(u.Host, r.Host) && u.User == nil
}

func secureHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/upload" {
			w.Header().Set("Cache-Control", "no-store")
		}
		if !sameOrigin(r) {
			http.Error(w, "Forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func rejectLimitedLogin(w http.ResponseWriter, mgr *auth.AuthManager, ip string) bool {
	if retry := mgr.LoginRetryAfter(ip); retry > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(int(retry.Seconds())+1))
		writeJSONError(w, 429, "登入嘗試過多，請於 60 秒後重試")
		return true
	}
	return false
}
