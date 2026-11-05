package native

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestWorkspaceWindows(t *testing.T) {
	if os.Getenv("NANODESK_TEST_WORKSPACE_GUI") != "1" {
		t.Skip("requires a Windows desktop; set NANODESK_TEST_WORKSPACE_GUI=1")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	user := syscall.NewLazyDLL("user32.dll")
	call := func(name string, args ...uintptr) uintptr {
		value, _, _ := user.NewProc(name).Call(args...)
		return value
	}
	classWindow := func(parent uintptr, class string) uintptr {
		name, _ := syscall.UTF16PtrFromString(class)
		for after := uintptr(0); ; {
			window := call("FindWindowExW", parent, after, uintptr(unsafe.Pointer(name)), 0)
			if window == 0 {
				return 0
			}
			var process uint32
			call("GetWindowThreadProcessId", window, uintptr(unsafe.Pointer(&process)))
			if process == uint32(os.Getpid()) {
				return window
			}
			after = window
		}
	}
	message := func(window, msg, w, l uintptr) uintptr {
		return call("SendMessageW", window, msg, w, l)
	}
	checkCount := func(window, expected uintptr) {
		t.Helper()
		if got := message(window, 0x1304, 0, 0); got != expected {
			t.Fatalf("tab count = %d, want %d", got, expected)
		}
	}
	type rect struct{ left, top, right, bottom int32 }
	tabPoint := func(bar, index uintptr) (int32, int32) {
		t.Helper()
		var r rect
		if message(bar, 0x130a, index, uintptr(unsafe.Pointer(&r))) == 0 {
			t.Fatalf("tab %d has no rectangle", index)
		}
		return (r.left + r.right) / 2, (r.top + r.bottom) / 2
	}
	mousePoint := func(x, y int32) uintptr {
		return uintptr(uint16(x)) | uintptr(uint16(y))<<16
	}

	if !Init() {
		t.Fatal("could not start Win32 UI")
	}
	main := classWindow(0, "NDMain")
	defer func() {
		for _, sid := range []string{"local", "first", "second", "third"} {
			Close(sid)
		}
		InboundBegin()
		InboundEnd()
		message(main, 0x10, 0, 0)
	}()
	bar := classWindow(main, "SysTabControl32")
	checkCount(bar, 1)
	Open("local", "1234567890")
	if !BindSession("local", "first") {
		t.Fatal("could not bind pending tab")
	}
	Open("second", "1234567891")
	Open("third", "1234567892")
	checkCount(bar, 4)

	x, y := tabPoint(bar, 1)
	message(bar, 0x201, 1, mousePoint(x, y))
	message(bar, 0x202, 0, mousePoint(x, y))
	Authenticated("first", 2)
	var original uintptr
	name, _ := syscall.UTF16PtrFromString("NDRemote")
	for after := uintptr(0); ; {
		child := call("FindWindowExW", main, after, uintptr(unsafe.Pointer(name)), 0)
		if child == 0 {
			break
		}
		if call("IsWindowVisible", child) != 0 {
			if original != 0 {
				t.Fatal("more than one docked canvas is visible")
			}
			original = child
		}
		after = child
	}
	if original == 0 {
		t.Fatal("selected workspace has no visible session view")
	}

	message(bar, 0x201, 1, mousePoint(x, y))
	call("PostMessageW", bar, 0x100, 0x1b, 0)
	Poll()
	message(bar, 0x202, 0, mousePoint(-100, y))
	checkCount(bar, 4)

	message(bar, 0x201, 1, mousePoint(x, y))
	message(bar, 0x202, 0, mousePoint(-100, y))
	floating := classWindow(0, "NDDetached")
	if floating == 0 || classWindow(floating, "NDRemote") != original {
		t.Fatal("drag did not move the existing session view into a window")
	}
	checkCount(bar, 3)
	message(floating, 0x112, 0x1ff0, 0)
	checkCount(bar, 4)
	if got := call("GetParent", original); got != main {
		t.Fatal("returning the tab did not preserve the session view")
	}

	InboundBegin()
	InboundAdd("in-a", "2000000001", "view", false)
	InboundAdd("in-b", "2000000002", "control", true)
	InboundEnd()
	inbound := classWindow(0, "NDInbound")
	if inbound == 0 || call("GetForegroundWindow") == inbound {
		t.Fatal("inbound window was missing or stole the foreground")
	}
	inboundBar := classWindow(inbound, "SysTabControl32")
	checkCount(inboundBar, 2)
	ChatText("in-b", "only for B", false)
	history := call("GetDlgItem", inbound, 501)
	historyText := func() string {
		buf := make([]uint16, 128)
		call("GetWindowTextW", history, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		return syscall.UTF16ToString(buf)
	}
	before := historyText()
	x, y = tabPoint(inboundBar, 1)
	message(inboundBar, 0x201, 1, mousePoint(x, y))
	message(inboundBar, 0x202, 0, mousePoint(x, y))
	if after := historyText(); after == before {
		t.Fatal("selecting another incoming session did not switch chat history")
	}
	message(inbound, 0x111, 504, 0)
	for {
		event, result := Poll()
		if result <= 0 {
			t.Fatal("single-session disconnect did not emit an event")
		}
		if event.Kind == 42 {
			if event.Session != "in-b" {
				t.Fatalf("disconnect targeted %q", event.Session)
			}
			break
		}
	}
	InboundBegin()
	InboundEnd()
	if classWindow(0, "NDInbound") != 0 {
		t.Fatal("inbound window remained after the last session ended")
	}
}
