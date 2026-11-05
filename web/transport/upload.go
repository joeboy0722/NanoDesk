package transport

import (
	"encoding/json"
	"fmt"
	"io"
	"kvm/core/auth"
	"kvm/core/desktop"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var uploadSlots = make(chan struct{}, 4)

func uploadLimitLabel(limit int64) string { return fmt.Sprintf("%d GiB", limit/(1<<30)) }

type uploadResult struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

type sessionReader struct {
	io.Reader
	mgr     *auth.AuthManager
	token   string
	request *http.Request
}

func (s sessionReader) Read(p []byte) (int, error) {
	if err := s.request.Context().Err(); err != nil {
		return 0, err
	}
	if role, ok := s.mgr.ValidateToken(s.token); !ok || role < auth.RoleStandard {
		return 0, fmt.Errorf("連線授權已失效")
	}
	return s.Reader.Read(p)
}

func (h *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	limit := desktop.UploadLimit()
	if r.ContentLength > limit {
		writeJSONError(w, 413, "含傳輸封裝不可超過 "+uploadLimitLabel(limit))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", 405)
		return
	}
	token := r.Header.Get("X-Auth-Token")
	role, ok := h.authMgr.ValidateToken(token)
	if !ok || role < auth.RoleStandard {
		writeJSONError(w, 403, "未具備檔案傳輸權限")
		return
	}
	select {
	case uploadSlots <- struct{}{}:
		defer func() { <-uploadSlots }()
	default:
		writeJSONError(w, 429, "傳輸數已滿")
		return
	}
	dir, err := desktop.DesktopPath()
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	h.receiveUpload(w, r, token, dir, limit)
}

func (h *Server) receiveUpload(w http.ResponseWriter, r *http.Request, token, dir string, limit int64) {
	controller := http.NewResponseController(w)
	defer controller.SetReadDeadline(time.Time{})
	// Refresh the network deadline on body reads, including multipart headers.
	r.Body = &uploadBody{ReadCloser: r.Body, controller: controller}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	reader, err := r.MultipartReader()
	if err != nil {
		writeJSONError(w, 400, "不合法的上傳格式")
		return
	}
	results := make([]uploadResult, 0)
	status := "ok"
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			results = append(results, uploadResult{Error: "上傳中斷、格式錯誤或超過 " + uploadLimitLabel(limit)})
			status = "error"
			break
		}
		name := part.FileName()
		if name == "" {
			part.Close()
			continue
		}
		result := uploadResult{Name: name}
		if len(results) >= 100 {
			part.Close()
			results = append(results, uploadResult{Name: name, Error: "單次最多 100 個檔案"})
			status = "error"
			break
		}
		result.Path, err = h.saveUpload(r, token, dir, name, part)
		if err != nil {
			result.Error = err.Error()
			status = "error"
		}
		results = append(results, result)
		part.Close()
		if err != nil {
			break
		}
	}
	if len(results) == 0 {
		status = "error"
		results = append(results, uploadResult{Error: "沒有檔案"})
	}
	w.Header().Set("Content-Type", "application/json")
	if status != "ok" {
		w.WriteHeader(http.StatusBadRequest)
	}
	json.NewEncoder(w).Encode(struct {
		Status  string         `json:"status"`
		Results []uploadResult `json:"results"`
	}{status, results})
}

type uploadBody struct {
	io.ReadCloser
	controller *http.ResponseController
}

func (b *uploadBody) Read(p []byte) (int, error) {
	b.controller.SetReadDeadline(time.Now().Add(30 * time.Second))
	return b.ReadCloser.Read(p)
}

func (h *Server) saveUpload(r *http.Request, token, dir, name string, part io.Reader) (string, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name || strings.ContainsAny(name, ":\\/") {
		return "", fmt.Errorf("不合法的檔名")
	}
	tmp, err := os.CreateTemp(dir, ".nanodesk-upload-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, sessionReader{part, h.authMgr, token, r})
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = r.Context().Err(); err != nil {
		return "", err
	}
	if role, ok := h.authMgr.ValidateToken(token); !ok || role < auth.RoleStandard {
		return "", fmt.Errorf("連線授權已失效")
	}
	return desktop.CommitUpload(tmp.Name(), dir, name)
}
