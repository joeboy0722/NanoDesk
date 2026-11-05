package auth

import (
	"testing"
)

// 測試自訂密碼支援英文字母、數字與特殊符號之驗證與權限核發
func TestCustomPasswordsWithSpecialCharacters(t *testing.T) {
	mgr := NewAuthManager(nil)

	// 設定包含特殊符號、英文大小寫、數字之自訂密碼
	viewPwd := "View#2026!@$"
	stdPwd := "Std_Control^888&"
	adminPwd := "Admin*P@$$w0rd!#%999"

	mgr.InitPasswords(viewPwd, stdPwd, adminPwd)

	v, s, a := mgr.GetPasswords()
	if v != viewPwd || s != stdPwd || a != adminPwd {
		t.Fatalf("密碼讀取與設定不符：預期 (%s, %s, %s)，得到 (%s, %s, %s)", viewPwd, stdPwd, adminPwd, v, s, a)
	}

	// 驗證觀看者密碼
	role, token, ok := mgr.AuthenticateByPassword(viewPwd, "127.0.0.1")
	if !ok || role != RoleView || token == "" {
		t.Errorf("觀看者密碼驗證失敗：ok=%v, role=%v", ok, role)
	}

	// 驗證一般控制密碼
	role, token, ok = mgr.AuthenticateByPassword(stdPwd, "127.0.0.1")
	if !ok || role != RoleStandard || token == "" {
		t.Errorf("一般控制密碼驗證失敗：ok=%v, role=%v", ok, role)
	}

	// 驗證完全控制(管理員)密碼
	role, token, ok = mgr.AuthenticateByPassword(adminPwd, "127.0.0.1")
	if !ok || role != RoleAdmin || token == "" {
		t.Errorf("管理員密碼驗證失敗：ok=%v, role=%v", ok, role)
	}

	// 驗證錯誤密碼應被拒絕
	role, _, ok = mgr.AuthenticateByPassword("WrongPassword123!", "127.0.0.1")
	if ok || role != RoleNone {
		t.Errorf("錯誤密碼不應驗證通過：ok=%v, role=%v", ok, role)
	}

	// 驗證空密碼應被拒絕
	role, _, ok = mgr.AuthenticateByPassword("", "127.0.0.1")
	if ok || role != RoleNone {
		t.Errorf("空密碼不應驗證通過：ok=%v, role=%v", ok, role)
	}
}

// 測試動態熱更新密碼
func TestDynamicSetPassword(t *testing.T) {
	mgr := NewAuthManager(nil)

	newAdmin := "New_Admin_#2026_HotReload!"
	mgr.SetPassword(RoleAdmin, newAdmin)

	role, _, ok := mgr.AuthenticateByPassword(newAdmin, "127.0.0.1")
	if !ok || role != RoleAdmin {
		t.Errorf("熱更新密碼驗證失敗：ok=%v, role=%v", ok, role)
	}
}
