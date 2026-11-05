package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"kvm/app/engine"
	"kvm/app/native"
	"kvm/core/capture"
	"kvm/core/clipboard"
	"kvm/core/identity"
	"kvm/core/input"
)

type viewState struct {
	myID       string
	controller bool
	active     bool
	clip       bool
	monitors   []capture.Monitor
	current    int
	numbers    map[string]int
	next       int
	role       int
}

func main() {
	runtime.LockOSThread()
	defer func() {
		if r := recover(); r != nil {
			native.Error(fmt.Sprintf("NanoDesk 發生錯誤：%v", r))
		}
	}()
	unlock, err := identity.Lock()
	if err != nil {
		native.Error(err.Error())
		return
	}
	defer unlock()
	state, err := identity.Load()
	if err != nil {
		native.Error(err.Error())
		return
	}
	e, err := engine.New(state)
	if err != nil {
		native.Error(err.Error())
		return
	}
	defer e.Close()
	if !native.Init() {
		native.Error("無法初始化 Win32／Media Foundation")
		return
	}
	native.Settings(state.Settings)
	if state.LastID != "" {
		native.MainText(0, state.LastID)
	}
	e.Start()
	views := map[string]*viewState{}
	var pendingID, pendingTab string
	var pendingClosed bool
	var pendingNumber int
	failPending := func(message string) {
		if pendingTab != "" && !pendingClosed {
			native.Text(pendingTab, message)
			native.Close(pendingTab)
		}
		pendingID, pendingTab, pendingClosed = "", "", false
	}
	var inboundSessions []engine.InboundSession
	var nextInbound time.Time
	updateInbound := func() {
		nextInbound = time.Now().Add(250 * time.Millisecond)
		sessions := e.InboundSessions()
		if slices.Equal(sessions, inboundSessions) {
			return
		}
		inboundSessions = sessions
		native.InboundBegin()
		for _, session := range sessions {
			native.InboundAdd(session.Session, session.Device, session.Role, session.Controller)
		}
		native.InboundEnd()
	}
	inbound := 0
	summary := func() { native.MainText(2, fmt.Sprintf("連入：%d 台　連出：%d 台", inbound, len(views))) }
	lastClipboard := clipboard.GetSequenceNumber()
	clipTimer := time.Now()
	suppressedSeq := uint32(0)
	send := func(sid string, m any) {
		b, er := json.Marshal(m)
		if er == nil {
			e.Command(sid, b)
		}
	}
	monitors := func(sid string, v *viewState) {
		native.ClearMonitors(sid)
		for i, m := range v.monitors {
			key := m.Device
			if key == "" {
				key = fmt.Sprint(m.Index)
			}
			n := v.numbers[key]
			if n == 0 {
				v.next++
				n = v.next
				v.numbers[key] = n
			}
			native.Monitor(sid, i, n, m.Width, m.Height, m.IsPrimary, i == v.current)
		}
	}
	for {
		ev, result := native.Poll()
		if result < 0 {
			return
		}
		if time.Now().After(nextInbound) {
			updateInbound()
		}
		if result > 0 {
			switch ev.Kind {
			case 1:
				if pendingTab != "" {
					native.Error("正在等待伺服器確認上一個連線")
					break
				}
				target := strings.NewReplacer(" ", "", "-", "").Replace(ev.Text)
				if err = e.Connect(target); err != nil {
					native.Error(err.Error())
					break
				}
				pendingNumber++
				pendingID, pendingTab = target, fmt.Sprintf("pending-%d", pendingNumber)
				native.Open(pendingTab, pendingID)
			case 2:
				clipboard.WriteText(ev.Text)
			case 3:
				return
			case 4:
				e.DisconnectAll()
			case 5:
				e.DisplayChanged()
			case 10:
				var s identity.Settings
				if err = json.Unmarshal([]byte(ev.Text), &s); err == nil {
					err = e.SaveSettings(s)
				}
				if err != nil {
					native.Error("無法儲存設定：" + err.Error())
				} else {
					native.Settings(s)
					native.MainText(1, "設定已儲存")
					if ev.A != 0 {
						native.MainText(4, "")
					}
				}
			case 11:
				native.Settings(e.Settings())
			case 20:
				kind := "mouse_move"
				if ev.A != 0 {
					kind = "laser_move"
				}
				send(ev.Session, map[string]any{"type": kind, "x": ev.X, "y": ev.Y})
			case 21:
				send(ev.Session, map[string]any{"type": "mouse_button", "button": ev.A, "isDown": ev.B != 0})
			case 22:
				send(ev.Session, map[string]any{"type": "mouse_wheel", "deltaX": ev.A, "deltaY": ev.B})
			case 23:
				if code := keyCode(ev.A, ev.X != 0, int(ev.Y)); code != "" {
					send(ev.Session, map[string]any{"type": "key", "code": code, "isDown": ev.B != 0})
				}
			case 24:
				send(ev.Session, map[string]any{"type": "release_all_keys"})
			case 25:
				e.Command(ev.Session, []byte(ev.Text))
			case 26:
				send(ev.Session, map[string]any{"type": "auth_login", "password": ev.Text})
			case 27:
				if err = e.SendFiles(ev.Session, strings.Split(ev.Text, "\n")); err != nil {
					native.Error(err.Error())
				}
			case 28:
				if ev.Session == pendingTab {
					pendingClosed = true
					native.Forget(ev.Session)
					break
				}
				e.Disconnect(ev.Session)
				delete(views, ev.Session)
				native.Forget(ev.Session)
				summary()
			case 29:
				if v := views[ev.Session]; v != nil {
					v.clip = ev.A != 0
					send(ev.Session, map[string]any{"type": "clipboard_auto", "enabled": v.clip && v.active})
				}
			case 30:
				if b, er := clipboard.ReadImagePNG(); er == nil && len(b) > 0 {
					send(ev.Session, map[string]any{"type": "clipboard_image_paste", "data": base64.StdEncoding.EncodeToString(b)})
				} else if text, er := clipboard.ReadText(); er == nil {
					send(ev.Session, map[string]any{"type": "clipboard_paste", "text": text})
				}
			case 31:
				e.CancelFiles(ev.Session)
			case 32:
				if v := views[ev.Session]; v != nil {
					v.active = ev.A != 0
					send(ev.Session, map[string]any{"type": "clipboard_auto", "enabled": v.clip && v.active})
				}
			case 41:
				if v := views[ev.Session]; v != nil && v.role == 0 {
					break
				}
				if err := e.SendChat(ev.Session, ev.Text); err != nil {
					native.ChatError(ev.Session, err.Error())
				} else {
					native.ChatText(ev.Session, ev.Text, true)
				}
			case 42:
				e.Disconnect(ev.Session)
			case 40:
				e.Approve(ev.Session, ev.Text, ev.A)
			}
		}
		// Bound work per UI tick so video cannot starve native messages.
		for i := 0; i < 16; i++ {
			select {
			case msg := <-e.Events:
				switch msg.Kind {
				case "id":
					native.MainText(0, msg.Text)
				case "status":
					native.MainText(1, msg.Text)
					if pendingTab != "" && msg.Text != "線上" {
						failPending(msg.Text)
					}
				case "chat":
					updateInbound()
					native.ChatText(msg.Session, msg.Text, false)
				case "inbound":
					updateInbound()
					inbound, _ = strconv.Atoi(msg.Text)
					summary()
				case "signal_error", "connect_error":
					if pendingTab != "" && (msg.Kind == "signal_error" || msg.Session == pendingID) {
						failPending(msg.Text)
					} else {
						native.Error(msg.Text)
					}
				case "error":
					native.Error(msg.Text)
				case "open":
					if pendingTab != "" && pendingID == msg.Text {
						tab, cancelled := pendingTab, pendingClosed
						pendingID, pendingTab, pendingClosed = "", "", false
						if cancelled || !native.BindSession(tab, msg.Session) {
							e.Disconnect(msg.Session)
							break
						}
					} else {
						native.Open(msg.Session, msg.Text)
					}
					views[msg.Session] = &viewState{numbers: map[string]int{}, clip: e.Settings().Clipboard}
					summary()
				case "ready":
					native.Text(msg.Session, "通道已就緒")
				case "peer_status", "file_status":
					native.Text(msg.Session, msg.Text)
				case "closed":
					native.Close(msg.Session)
					delete(views, msg.Session)
					summary()
				case "approval":
					parts := strings.SplitN(msg.Text, "|", 3)
					if len(parts) == 3 {
						native.Approval(msg.Session, parts[0], parts[1]+" / "+parts[2])
					}
				case "approval_close":
					native.CloseApproval(msg.Session)
				case "video":
					if native.Frame(msg.Session, msg.Data) < 0 {
						send(msg.Session, map[string]any{"type": "request_keyframe"})
					}
				case "control":
					v := views[msg.Session]
					if v == nil {
						break
					}
					var m struct {
						Type, ID, Name, Color, Message, Text, Data string
						Role                                       int
						Monitors                                   []capture.Monitor
						Current                                    int
						ControllerID                               string `json:"controller_id"`
						LocalActive                                bool   `json:"local_active"`
						Members                                    []struct {
							Name         string
							RoleName     string `json:"role_name"`
							IsController bool   `json:"is_controller"`
						}
						X, Y float64
					}
					if json.Unmarshal(msg.Data, &m) != nil {
						break
					}
					switch m.Type {
					case "auth_success":
						v.role = m.Role
						native.Authenticated(msg.Session, m.Role)
						native.Clipboard(msg.Session, v.clip)
						native.Text(msg.Session, fmt.Sprintf("已授權：%d", m.Role))
						send(msg.Session, map[string]any{"type": "clipboard_auto", "enabled": v.clip && v.active})
					case "session_chat":
						if v.role > 0 && engine.ValidChat(m.Text) {
							native.ChatText(msg.Session, m.Text, false)
						}
					case "init":
						valid := len(m.Monitors) > 0 && len(m.Monitors) <= 64
						for _, mon := range m.Monitors {
							if mon.Width <= 0 || mon.Height <= 0 || mon.Width > 16384 || mon.Height > 16384 {
								valid = false
							}
						}
						if !valid {
							native.Text(msg.Session, "對方傳來無效螢幕資訊")
							e.Disconnect(msg.Session)
							break
						}
						v.monitors = m.Monitors
						v.current = m.Current
						monitors(msg.Session, v)
					case "my_info":
						v.myID = m.ID
					case "control_state":
						v.controller = v.myID != "" && m.ControllerID == v.myID && !m.LocalActive
						native.Control(msg.Session, v.controller, m.LocalActive)
					case "auth_fail":
						native.Text(msg.Session, "通道已就緒")
						native.Text(msg.Session, m.Message)
					case "operation_error", "control_denied":
						native.Text(msg.Session, m.Message)
					case "clipboard_sync", "clipboard_data":
						if v.active && v.clip && v.controller && native.IsActive(msg.Session) {
							clipboard.WriteText(m.Text)
							suppressedSeq = clipboard.GetSequenceNumber()
						}
					case "clipboard_image_sync":
						if v.active && v.clip && v.controller && native.IsActive(msg.Session) {
							if b, er := base64.StdEncoding.DecodeString(m.Data); er == nil {
								clipboard.WriteImagePNG(b)
								suppressedSeq = clipboard.GetSequenceNumber()
							}
						}
					case "members_update":
						var lines []string
						for _, member := range m.Members {
							s := member.Name + " — " + member.RoleName
							if member.IsController {
								s += "（主控者）"
							}
							lines = append(lines, s)
						}
						native.Panel(msg.Session, strings.Join(lines, "\r\n"), true)
					case "laser_pointer":
						native.Laser(msg.Session, m.ID, m.Name, m.Color, m.X, m.Y)
					}
				}
			default:
				i = 16
			}
		}
		for i := 0; i < 3; i++ {
			select {
			case frame := <-e.Frames:
				if native.Frame(frame.Session, frame.Data) < 0 {
					send(frame.Session, map[string]any{"type": "request_keyframe"})
				}
			default:
				i = 3
			}
		}
		if time.Since(clipTimer) > 300*time.Millisecond {
			clipTimer = time.Now()
			seq := clipboard.GetSequenceNumber()
			if seq != lastClipboard {
				lastClipboard = seq
				if seq != suppressedSeq {
					for sid, v := range views {
						if v.active && v.clip && v.controller && native.IsActive(sid) {
							if b, er := clipboard.ReadImagePNG(); er == nil && len(b) > 0 {
								send(sid, map[string]any{"type": "clipboard_image_upload", "data": base64.StdEncoding.EncodeToString(b)})
							} else if text, er := clipboard.ReadText(); er == nil {
								send(sid, map[string]any{"type": "clipboard_sync", "text": text})
							}
							break
						}
					}
				}
			}
		}
		if result == 0 {
			time.Sleep(8 * time.Millisecond)
		}
	}
}
func keyCode(vk int, extended bool, scan int) string {
	if vk >= 0x41 && vk <= 0x5a {
		return "Key" + string(rune(vk))
	}
	if vk >= 0x30 && vk <= 0x39 {
		return "Digit" + string(rune(vk))
	}
	if vk >= 0x70 && vk <= 0x87 {
		return fmt.Sprintf("F%d", vk-0x6f)
	}
	if vk >= 0x60 && vk <= 0x69 {
		return fmt.Sprintf("Numpad%d", vk-0x60)
	}
	switch vk {
	case 16:
		if scan == 0x36 {
			return "ShiftRight"
		}
		return "ShiftLeft"
	case 17:
		if extended {
			return "ControlRight"
		}
		return "ControlLeft"
	case 18:
		if extended {
			return "AltRight"
		}
		return "AltLeft"
	case 13:
		if extended {
			return "NumpadEnter"
		}
		return "Enter"
	}
	codes := []string{"Backspace", "Tab", "Escape", "Space", "PageUp", "PageDown", "End", "Home", "ArrowLeft", "ArrowUp", "ArrowRight", "ArrowDown", "Insert", "Delete", "CapsLock", "NumLock", "ScrollLock", "MetaLeft", "MetaRight", "ContextMenu", "Semicolon", "Equal", "Comma", "Minus", "Period", "Slash", "Backquote", "BracketLeft", "Backslash", "BracketRight", "Quote", "NumpadMultiply", "NumpadAdd", "NumpadSubtract", "NumpadDecimal", "NumpadDivide", "PrintScreen", "Pause"}
	for _, c := range codes {
		if value, ok := input.CodeToVK(c); ok && int(value) == vk {
			return c
		}
	}
	return ""
}
