package capture

// Monitor 螢幕硬體資訊結構
type Monitor struct {
	Index     int    `json:"index"`      // 螢幕索引號 (0, 1, 2...)
	Device    string `json:"device"`     // 設備名稱 (如 \\.\DISPLAY1)
	X         int    `json:"x"`          // 桌面虛擬座標 X
	Y         int    `json:"y"`          // 桌面虛擬座標 Y
	Width     int    `json:"width"`      // 螢幕寬度像素
	Height    int    `json:"height"`     // 螢幕高度像素
	Rotation  int    `json:"rotation"`   // 旋轉角度 (0: 正常, 1: 90°, 2: 180°, 3: 270°)
	IsPrimary bool   `json:"is_primary"` // 是否為主要顯示器
}

// Frame 捕獲到的單一畫面訊框
type Frame struct {
	Width     int    // 訊框寬度
	Height    int    // 訊框高度
	Data      []byte // 原始 32-bit BGRA/RGBA 像素緩衝區
	Pitch     int    // 每一列 (Row) 的位元組步長
	Timestamp int64  // 捕獲的時間戳記 (微秒)
}

// Capturer 螢幕擷取引擎的統一抽象介面
type Capturer interface {
	// 初始化擷取引擎
	Init() error

	// 取得系統目前所有活動顯示器清單
	GetMonitors() []Monitor

	// 擷取指定顯示器的最新一幀畫面
	CaptureFrame(monitorIdx int) (*Frame, error)

	// 釋放引擎佔用的系統與 GPU 資源
	Close() error
}
