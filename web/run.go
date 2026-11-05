package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io/fs"
	"kvm/core/auth"
	"kvm/core/config"
	"kvm/core/desktop"
	"kvm/core/encoder"
	"kvm/web/hostgui"
	"kvm/web/transport"
	"net"
	"net/http"
	"runtime"
	"time"
)

func runWeb(webSubFS fs.FS) {
	// 全域未捕捉異常守護 (Panic Recovery)
	defer func() {
		if r := recover(); r != nil {
			stackBuf := make([]byte, 4096)
			n := runtime.Stack(stackBuf, false)
			gui.ShowFatalError("NanoDesk - 系統致命錯誤 (Panic)",
				"應用程式發生未預期的崩潰例外：\n\n%v\n\n【呼叫堆疊資訊】:\n%s",
				r, string(stackBuf[:n]))
		}
	}()

	var port int
	var flagPwd string
	bindIP := flag.String("bind", "", "監聽 IP（預設所有網卡；可設 127.0.0.1）")
	modeHTTPS := flag.Bool("https", false, "啟用記憶體自簽 HTTPS 模式 (預設為純 HTTP 模式)")
	flag.IntVar(&port, "port", 8080, "服務監聽埠號 (支援 --port 或 -port)")
	flag.IntVar(&port, "p", 8080, "服務監聽埠號簡寫 (支援 -p 或 --p)")
	flag.StringVar(&flagPwd, "pwd", "", "自訂全權限存取密碼 (支援英文字母、數字與特殊符號)")
	flag.Parse()
	addr := net.JoinHostPort(*bindIP, fmt.Sprint(port))

	// 0. 外部依賴檢查：Windows Media Foundation (mfplat.dll / mfreadwrite.dll)
	if err := encoder.CheckMediaFoundationAvailable(); err != nil {
		gui.ShowFatalError("NanoDesk - 缺少系統媒體元件", "%v", err)
		return
	}

	// 1. 外部通訊檢查：預先驗證網路通訊埠是否被佔用
	testLn, err := net.Listen("tcp", addr)
	if err != nil {
		gui.ShowFatalError("NanoDesk - 通訊埠綁定失敗",
			"無法監聽連接埠 %d：%v\n\n💡 可能原因：\n1. 該通訊埠已被其他程式（或前一次尚未完全結束的 NanoDesk）佔用。\n2. 防火牆或安全性原則阻止監聽。\n\n建議：請檢查工作管理員是否有殘留的 NanoDesk.exe，或在命令列加入 -port <其他埠號> 參數啟動。",
			port, err)
		return
	}
	testLn.Close()

	ips := transport.LocalIPs()
	if *bindIP != "" {
		ips = []string{*bindIP}
	}
	primaryIP := "localhost"
	if len(ips) > 0 {
		primaryIP = ips[len(ips)-1]
	}
	serverProtocol := "http"
	if *modeHTTPS {
		serverProtocol = "https"
	}
	serverURL := fmt.Sprintf("%s://%s:%d", serverProtocol, primaryIP, port)

	// 2. 建立串流中樞 (DXGI / GDI 畫面擷取 + 螢幕檢測 + H.264 編碼器)
	var hostGUI *gui.HostGUI
	authMgr := auth.NewAuthManager(func(req *auth.AuthRequest) {
		if hostGUI != nil {
			hostGUI.ShowAuthPrompt(req)
		}
	})

	hub, err := desktop.NewHost(authMgr, nil)
	if err != nil {
		gui.ShowFatalError("NanoDesk - 畫面擷取引擎啟動失敗",
			"無法初始化桌面畫面擷取引擎：\n%v\n\n💡 常見排錯提示：\n1. 若您是在 Windows Server / 雲端虛擬機 (EC2/Azure/GCP) 運行，伺服器可能處於無螢幕 (Headless) 狀態，或遠端桌面 (RDP) 處於鎖定/斷開狀態。\n2. 系統未安裝相容之顯示卡驅動程式或不支援 DirectX 11 / GDI 桌面複製。\n3. 請確認伺服器有連接螢幕、或啟用虛擬螢幕 (Virtual Display Adapter)。",
			err)
		return
	}
	defer hub.Close()

	// 2. 初始化三級權限與雙軌審核安全管理器

	// 載入本地設定檔 (config.json) 中的自訂密碼
	appCfg := config.LoadConfig()
	if appCfg.LoadError != nil {
		gui.ShowFatalError("設定檔錯誤", "%v", appCfg.LoadError)
		return
	}
	uploadGiB := appCfg.UploadLimitGiB
	if uploadGiB == 0 {
		uploadGiB = 1
	} // Settings written before this option existed.
	limit, limitErr := desktop.UploadLimitBytes(uploadGiB)
	if limitErr != nil {
		gui.ShowFatalError("傳輸大小設定錯誤", "%v", limitErr)
		return
	}
	desktop.SetUploadLimitBytes(limit)
	if flagPwd != "" {
		// 若透過命令列指定，直接套用全權限密碼
		if err := authMgr.SetPassword(auth.RoleAdmin, flagPwd); err != nil {
			gui.ShowFatalError("密碼設定失敗", "%v", err)
			return
		}
	} else if appCfg.RememberPasswords && appCfg.Loaded {
		// 載入已儲存之自訂密碼 (支援英數字及各類特殊符號)
		if err := authMgr.ApplyPasswords(appCfg.ViewPassword, appCfg.StandardPassword, appCfg.AdminPassword); err != nil {
			gui.ShowFatalError("密碼設定失敗", "%v", err)
			return
		}
	}

	// 3. 啟動被控端純 Win32 原生管理 GUI (零體積膨脹，AnyDesk 原生質感)
	serverURLs := []string{serverURL}
	for _, ip := range ips {
		url := fmt.Sprintf("%s://%s:%d", serverProtocol, ip, port)
		if url != serverURL {
			serverURLs = append(serverURLs, url)
		}
	}
	serverURLs = append(serverURLs, fmt.Sprintf("%s://localhost:%d", serverProtocol, port))
	hostGUI = gui.StartHostGUI(authMgr, serverURLs, func() {
		hub.KickAll()
	}, func() { go hub.RecoverDisplay() }, gui.UploadSettings{
		GiB: uploadGiB,
		Apply: func(gib int64) error {
			limit, err := desktop.UploadLimitBytes(gib)
			if err != nil {
				return err
			}
			cfg := config.LoadConfig()
			if cfg.LoadError != nil {
				return cfg.LoadError
			}
			// Saving just the upload setting must preserve random passwords on next startup.
			if !cfg.Loaded {
				cfg.RememberPasswords = false
			}
			cfg.UploadLimitGiB = gib
			if err := config.SaveConfig(cfg); err != nil {
				return err
			}
			desktop.SetUploadLimitBytes(limit)
			hub.NotifyUploadLimit()
			return nil
		},
	})
	hub.SetUI(hostGUI)

	hub.Start()
	handler := transport.New(hub, authMgr).Handler(webSubFS)
	if *modeHTTPS {
		// HTTPS 模式
		cert, err := transport.MemoryCertificate()
		if err != nil {
			gui.ShowFatalError("NanoDesk - TLS 憑證生成失敗", "無法生成記憶體自簽 TLS 憑證：%v", err)
			return
		}

		server := &http.Server{
			Addr: addr, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, Handler: handler,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
			},
		}

		fmt.Printf("🔒 已啟動【HTTPS 加密模式】\n")
		fmt.Printf("👉 本機請開啟: https://localhost:%d\n", port)
		for _, ip := range ips {
			fmt.Printf("👉 測試筆電請開啟: https://%s:%d\n", ip, port)
		}
		fmt.Println("\n按下 Ctrl+C 可停止服務。")

		if err := server.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			gui.ShowFatalError("NanoDesk - HTTPS 服務異常中止", "HTTPS 伺服器監聽發生錯誤：%v", err)
		}
	} else {
		// 預設：純 HTTP 模式 (零憑證警告，雙軌解碼自適應)
		fmt.Printf("🌐 已啟動【純 HTTP 模式】(雙軌解碼：WebCodecs / MSE 自適應)\n")
		fmt.Printf("👉 本機瀏覽器請開: http://localhost:%d\n", port)
		for _, ip := range ips {
			fmt.Printf("👉 局域網其他電腦/筆電請開: http://%s:%d\n", ip, port)
		}
		fmt.Println("✨ 免裝憑證、免開 HTTPS、開箱即用！")
		fmt.Println("\n按下 Ctrl+C 可停止服務。")

		if err := (&http.Server{Addr: addr, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, Handler: handler}).ListenAndServe(); err != nil && err != http.ErrServerClosed {
			gui.ShowFatalError("NanoDesk - HTTP 服務異常中止", "HTTP 伺服器監聽發生錯誤：%v", err)
		}
	}
}
