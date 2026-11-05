// Package identity stores native APP state independently of the Web host.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

type Settings struct {
	RelayOnly        bool   `json:"relay_only"`
	RelayTLSOnly     bool   `json:"relay_tls_only"`
	Server           string `json:"server"`
	CAFile           string `json:"ca_file,omitempty"`
	Unattended       bool   `json:"unattended"`
	ViewPassword     string `json:"view_password,omitempty"`
	StandardPassword string `json:"standard_password,omitempty"`
	AdminPassword    string `json:"admin_password,omitempty"`
	UploadGiB        int64  `json:"upload_gib"`
	Clipboard        bool   `json:"clipboard"`
}
type State struct {
	Private  ed25519.PrivateKey `json:"private"`
	LastID   string             `json:"last_id"`
	Settings Settings           `json:"settings"`
	Path     string             `json:"-"`
}
type blob struct {
	N uint32
	P *byte
}

func protect(b []byte, decode bool) ([]byte, error) {
	if len(b) == 0 {
		return nil, fmt.Errorf("empty protected state")
	}
	in := blob{uint32(len(b)), &b[0]}
	var out blob
	name := "CryptProtectData"
	if decode {
		name = "CryptUnprotectData"
	}
	n, _, e := syscall.NewLazyDLL("crypt32.dll").NewProc(name).Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if n == 0 {
		return nil, e
	}
	defer syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree").Call(uintptr(unsafe.Pointer(out.P)))
	return append([]byte(nil), unsafe.Slice(out.P, int(out.N))...), nil
}
func Load() (*State, error) {
	dir, e := os.UserConfigDir()
	if e != nil {
		return nil, e
	}
	// UserConfigDir is roaming on Windows; device identity must remain local.
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dir = local
	}
	return LoadPath(filepath.Join(dir, "NanoDesk", "state.bin"))
}
func LoadPath(path string) (*State, error) {
	s := &State{Path: path, Settings: Settings{UploadGiB: 1, Clipboard: true}}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		_, s.Private, e = ed25519.GenerateKey(rand.Reader)
		if e == nil {
			e = s.Save()
		}
		return s, e
	}
	if e != nil {
		return nil, e
	}
	b, e = protect(b, true)
	if e != nil {
		return nil, fmt.Errorf("裝置身分無法解密，請保留備份後修復：%w", e)
	}
	if e = json.Unmarshal(b, s); e != nil {
		return nil, e
	}
	if len(s.Private) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid device key")
	}
	return s, nil
}
func (s *State) Save() error {
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	b, e = protect(b, false)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(s.Path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(s.Path), "state-*.tmp")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(name, s.Path)
}
func Lock() (func(), error) {
	// Local namespace plus SID scopes one identity to this user and login session.
	token, e := syscall.OpenCurrentProcessToken()
	if e != nil {
		return nil, e
	}
	defer token.Close()
	u, e := token.GetTokenUser()
	if e != nil {
		return nil, e
	}
	sid, e := u.User.Sid.String()
	if e != nil {
		return nil, e
	}
	name, _ := syscall.UTF16PtrFromString("Global\\NanoDesk-App-" + sid)
	h, _, err := syscall.NewLazyDLL("kernel32.dll").NewProc("CreateMutexW").Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return nil, err
	}
	if err == syscall.Errno(183) {
		syscall.CloseHandle(syscall.Handle(h))
		return nil, fmt.Errorf("NanoDesk APP 已經開啟")
	}
	return func() { syscall.CloseHandle(syscall.Handle(h)) }, nil
}
