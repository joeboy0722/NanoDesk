//go:build windows

package main

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
	"unsafe"
)

func TestWindowsGUIABIStructures(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("Windows x64 GUI")
	}
	for name, spec := range map[string]struct{ got, want uintptr }{"WNDCLASSEXW": {unsafe.Sizeof(serverWindowClass{}), 80}, "MSG": {unsafe.Sizeof(serverMessage{}), 48}, "TCITEMW": {unsafe.Sizeof(serverTabItem{}), 40}, "OPENFILENAMEW": {unsafe.Sizeof(fileDialog{}), 152}, "SCROLLINFO": {unsafe.Sizeof(scrollInfo{}), 28}, "RECT": {unsafe.Sizeof(screenRect{}), 16}} {
		if spec.got != spec.want {
			t.Errorf("%s size=%d want=%d", name, spec.got, spec.want)
		}
	}
}
func TestGUIBoundedLogBuffer(t *testing.T) {
	b := &logBuffer{}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Write([]byte(strings.Repeat("伺服器紀錄", 30) + "\n"))
			}
		}()
	}
	wg.Wait()
	s := b.Text()
	if len(s) > 60000 || !utf8.ValidString(s) {
		t.Fatal("unbounded or malformed log")
	}
}
