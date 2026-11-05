package identity

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestProtectedStableIdentityAndSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app", "state.bin")
	a, e := LoadPath(path)
	if e != nil {
		t.Fatal(e)
	}
	a.Settings.AdminPassword = "test-secret-password"
	a.LastID = "1234567890"
	if e = a.Save(); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte(a.Settings.AdminPassword)) || bytes.Contains(raw, a.Private) {
		t.Fatal("plaintext secret")
	}
	b, e := LoadPath(path)
	if e != nil || !bytes.Equal(a.Private, b.Private) || b.LastID != a.LastID || b.Settings.AdminPassword != a.Settings.AdminPassword {
		t.Fatal("identity changed", e)
	}
	if e = os.WriteFile(path, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadPath(path); e == nil {
		t.Fatal("silently reset damaged identity")
	}
}
