package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// 權限等級定義
type Role int

const (
	RoleNone     Role = 0 // 未授權 (禁止連線/僅能停留在驗證頁面)
	RoleView     Role = 1 // 第一級：僅觀看 (只能看畫面，禁止鍵鼠、剪貼簿與檔案傳輸)
	RoleStandard Role = 2 // 第二級：可控制 (一般權限，支援鍵鼠、剪貼簿、檔案傳輸，禁止特權組合鍵)
	RoleAdmin    Role = 3 // 第三級：可控制 (完全控制/系統管理員，解鎖所有特權功能與系統鍵)
)

// 將 Role 轉為易讀中文名稱
func (r Role) String() string {
	switch r {
	case RoleView:
		return "僅觀看"
	case RoleStandard:
		return "一般控制"
	case RoleAdmin:
		return "完全控制(管理員)"
	default:
		return "未授權"
	}
}

// 請求審核狀態
type RequestStatus int

const (
	StatusPending  RequestStatus = 0 // 等待被控端核准中
	StatusApproved RequestStatus = 1 // 已同意
	StatusRejected RequestStatus = 2 // 已拒絕
	StatusExpired  RequestStatus = 3 // 已過期
)

// 遠端連線授權請求
type AuthRequest struct {
	ID          string        `json:"id"`
	ClientIP    string        `json:"client_ip"`
	CreatedAt   time.Time     `json:"created_at"`
	Status      RequestStatus `json:"status"`
	GrantedRole Role          `json:"granted_role"`
	Token       string        `json:"token,omitempty"`
}

// 授權階段 Token 資訊
type Session struct {
	Token     string
	Role      Role
	ClientIP  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// 權限與安全管理器
type AuthManager struct {
	mu           sync.RWMutex
	viewPwd      string
	standardPwd  string
	adminPwd     string
	sessions     map[string]*Session     // token -> session
	requests     map[string]*AuthRequest // requestID -> request
	onRequestCB  func(req *AuthRequest)  // 當有新連線請求時的回呼函數 (通知 GUI 彈窗)
}

// 建立全新權限管理器
func NewAuthManager(onNewRequest func(req *AuthRequest)) *AuthManager {
	mgr := &AuthManager{
		sessions:    make(map[string]*Session),
		requests:    make(map[string]*AuthRequest),
		onRequestCB: onNewRequest,
	}
	mgr.GenerateRandomPasswords()
	return mgr
}

// 隨機產生 6 位數密碼字串
func generateRandomPIN() string {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "666888"
	}
	return fmt.Sprintf("%06d", n.Int64())
}

// 隨機重設三組獨立密碼
func (m *AuthManager) GenerateRandomPasswords() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.viewPwd = generateRandomPIN()
	m.standardPwd = generateRandomPIN()
	m.adminPwd = generateRandomPIN()
}

// 取得當前三組密碼
func (m *AuthManager) GetPasswords() (viewPwd, standardPwd, adminPwd string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.viewPwd, m.standardPwd, m.adminPwd
}

// 設定指定角色的密碼
func (m *AuthManager) SetPassword(role Role, newPwd string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch role {
	case RoleView:
		m.viewPwd = newPwd
	case RoleStandard:
		m.standardPwd = newPwd
	case RoleAdmin:
		m.adminPwd = newPwd
	}
}

// 透過密碼驗證權限，回傳對應授權之 Role 與 Session Token
func (m *AuthManager) AuthenticateByPassword(pwd string, clientIP string) (Role, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var role Role = RoleNone

	if pwd != "" {
		if pwd == m.adminPwd {
			role = RoleAdmin
		} else if pwd == m.standardPwd {
			role = RoleStandard
		} else if pwd == m.viewPwd {
			role = RoleView
		}
	}

	if role == RoleNone {
		return RoleNone, "", false
	}

	// 簽發 Session Token
	token := m.generateTokenLocked(role, clientIP)
	return role, token, true
}

// 產生隨機 Session Token (需在持有鎖時呼叫)
func (m *AuthManager) generateTokenLocked(role Role, clientIP string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)

	m.sessions[token] = &Session{
		Token:     token,
		Role:      role,
		ClientIP:  clientIP,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(24 * time.Hour), // 24小時有效期
	}

	return token
}

// 驗證 Token 是否有效，並回傳對應權限
func (m *AuthManager) ValidateToken(token string) (Role, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sess, exists := m.sessions[token]
	if !exists {
		return RoleNone, false
	}

	if time.Now().After(sess.ExpiresAt) {
		return RoleNone, false
	}

	return sess.Role, true
}

// 遠端客戶端發起「請求被控端核准」
func (m *AuthManager) CreateRequest(clientIP string) *AuthRequest {
	m.mu.Lock()

	b := make([]byte, 8)
	_, _ = rand.Read(b)
	reqID := hex.EncodeToString(b)

	req := &AuthRequest{
		ID:        reqID,
		ClientIP:  clientIP,
		CreatedAt: time.Now(),
		Status:    StatusPending,
	}

	m.requests[reqID] = req
	cb := m.onRequestCB
	m.mu.Unlock()

	// 觸發 GUI 彈窗通知
	if cb != nil {
		go cb(req)
	}

	return req
}

// 被控端審核請求：同意並給予指定權限
func (m *AuthManager) ApproveRequest(reqID string, role Role) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	req, exists := m.requests[reqID]
	if !exists || req.Status != StatusPending {
		return "", false
	}

	req.Status = StatusApproved
	req.GrantedRole = role
	req.Token = m.generateTokenLocked(role, req.ClientIP)

	return req.Token, true
}

// 被控端審核請求：拒絕連線
func (m *AuthManager) RejectRequest(reqID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	req, exists := m.requests[reqID]
	if !exists || req.Status != StatusPending {
		return false
	}

	req.Status = StatusRejected
	return true
}

// 查詢連線請求狀態 (供前端輪詢或長連線狀態確認)
func (m *AuthManager) GetRequestStatus(reqID string) (*AuthRequest, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	req, exists := m.requests[reqID]
	if !exists {
		return nil, false
	}

	// 超過 60 秒未審核自動過期
	if req.Status == StatusPending && time.Since(req.CreatedAt) > 60*time.Second {
		req.Status = StatusExpired
	}

	return req, true
}

// 撤銷指定 Token (中斷連線)
func (m *AuthManager) RevokeToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}

// 撤銷所有連線 Token (一鍵緊急中斷所有連線)
func (m *AuthManager) RevokeAllTokens() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions = make(map[string]*Session)
}
