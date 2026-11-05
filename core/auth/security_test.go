package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLoginLimitsAndPasswordRevocation(t *testing.T) {
	m := NewAuthManager(nil)
	if err := m.InitPasswords("view", "standard", "admin"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, _, ok := m.AuthenticateByPassword("wrong", "a"); ok {
			t.Fatal("wrong password accepted")
		}
	}
	if _, _, ok := m.AuthenticateByPassword("admin", "a"); ok {
		t.Fatal("blocked IP logged in")
	}
	if m.LoginRetryAfter("a") <= 0 {
		t.Fatal("missing cooldown")
	}
	_, token, ok := m.AuthenticateByPassword("admin", "b")
	if !ok {
		t.Fatal("unrelated IP blocked")
	}
	if err := m.ApplyPasswords("duplicate", "duplicate", "admin"); err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, ok := m.ValidateToken(token); !ok {
		t.Fatal("failed update revoked token")
	}
	if err := m.ApplyPasswords("view", "standard", "new"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.ValidateToken(token); ok {
		t.Fatal("password update retained token")
	}
	m.mu.Lock()
	m.failures["a"].BlockedUntil = time.Now().Add(-time.Second)
	m.failures["a"].Started = time.Now().Add(-2 * time.Minute)
	m.mu.Unlock()
	_, token, ok = m.AuthenticateByPassword("new", "a")
	if !ok {
		t.Fatal("cooldown did not expire")
	}
	m.mu.Lock()
	m.sessions[token].ExpiresAt = time.Now().Add(-time.Second)
	m.mu.Unlock()
	if _, ok := m.ValidateToken(token); ok {
		t.Fatal("expired session valid")
	}
	m.Cleanup()
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.sessions[token]; ok {
		t.Fatal("expired session retained")
	}
}

func TestApprovalLimitExpiryAndSnapshots(t *testing.T) {
	m := NewAuthManager(nil)
	first := m.CreateRequest("first")
	if m.CreateRequest("first").ID != first.ID {
		t.Fatal("same IP created another request")
	}
	first.Status = StatusApproved
	snapshot, _ := m.GetRequestStatus(first.ID)
	if snapshot.Status != StatusPending {
		t.Fatal("mutable request escaped")
	}
	for i := 0; i < 15; i++ {
		if m.CreateRequest(fmt.Sprint(i)) == nil {
			t.Fatal("limit reached early")
		}
	}
	if m.CreateRequest("overflow") != nil {
		t.Fatal("pending limit not enforced")
	}
	m.mu.Lock()
	m.requests[first.ID].CreatedAt = time.Now().Add(-61 * time.Second)
	m.mu.Unlock()
	if _, ok := m.ApproveRequest(first.ID, RoleAdmin); ok {
		t.Fatal("expired request approved")
	}
	if m.CreateRequest("overflow") == nil {
		t.Fatal("expired slot not reclaimed")
	}
	if _, ok := m.ApproveRequest(snapshot.ID, Role(99)); ok {
		t.Fatal("invalid role approved")
	}
}

func TestConcurrentRequestStatus(t *testing.T) {
	m := NewAuthManager(nil)
	req := m.CreateRequest("a")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.GetRequestStatus(req.ID)
				m.ApproveRequest(req.ID, RoleView)
				m.Cleanup()
			}
		}()
	}
	wg.Wait()
}
