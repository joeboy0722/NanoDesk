package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"kvm/core/desktop"
)

var transferSlots = make(chan struct{}, 4)

type fileMeta struct {
	Transfer string `json:"transfer,omitempty"`
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Count    int    `json:"count,omitempty"`
	Error    string `json:"error,omitempty"`
}
type receiver struct {
	mu              sync.Mutex
	peer            *Peer
	file            *os.File
	path, name, dir string
	remaining       int64
	batchRemaining  int64
	batchCount      int
	transfer        string
	updated         time.Time
	slot            bool
	timer           *time.Timer
}

func (r *receiver) reply(kind, message string) {
	r.peer.mu.Lock()
	d := r.peer.file
	r.peer.mu.Unlock()
	if d != nil {
		b, _ := json.Marshal(fileMeta{Type: kind, Error: message, Transfer: r.transfer})
		d.SendText(string(b))
	}
}
func (r *receiver) cleanup() {
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if r.file != nil {
		r.file.Close()
		os.Remove(r.path)
		r.file = nil
	}
	if r.slot {
		<-transferSlots
		r.slot = false
	}
}
func (r *receiver) close() { r.mu.Lock(); defer r.mu.Unlock(); r.cleanup() }
func (r *receiver) message(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p := r.peer
	if len(b) > 1 && b[0] == 0 && len(b) < 4096 {
		var header fileMeta
		if json.Unmarshal(b[1:], &header) == nil && len(header.Transfer) == 32 {
			r.transfer = header.Transfer
		}
	}
	p.mu.Lock()
	c := p.client
	p.mu.Unlock()
	if c == nil || p.engine.host.Load() == nil || !p.engine.host.Load().CanTransfer(c) {
		r.cleanup()
		r.reply("error", "檔案傳輸未授權")
		return
	}
	if len(b) == 0 {
		return
	}
	switch b[0] {
	case 0:
		var m fileMeta
		if len(b) > 4096 || json.Unmarshal(b[1:], &m) != nil {
			return
		}
		r.cleanup()
		if m.Type == "batch" {
			if len(m.Transfer) != 32 {
				r.reply("error", "傳輸識別碼無效")
				return
			}
			r.transfer = m.Transfer
			r.batchRemaining = 0
			r.batchCount = 0
			if m.Count < 1 || m.Count > 100 || m.Total < 0 || m.Total > desktop.UploadLimit() {
				r.reply("error", "每批傳輸上限無效")
				return
			}
			r.batchRemaining = m.Total
			r.batchCount = m.Count
			r.reply("batch_ready", "")
			return
		}
		if m.Type != "begin" || m.Transfer != r.transfer || m.Size < 0 || r.batchCount < 1 || m.Size > r.batchRemaining || !filepath.IsLocal(m.Name) || filepath.Base(m.Name) != m.Name {
			r.reply("error", "檔名或大小無效")
			return
		}
		r.batchRemaining -= m.Size
		r.batchCount--
		select {
		case transferSlots <- struct{}{}:
			r.slot = true
		default:
			r.reply("error", "同時傳輸數已滿")
			return
		}
		dir, e := p.engine.services.ReceiveDirectory()
		if e != nil {
			r.cleanup()
			r.reply("error", e.Error())
			return
		}
		f, e := os.CreateTemp(dir, ".nanodesk-*.part")
		if e != nil {
			r.cleanup()
			r.reply("error", e.Error())
			return
		}
		r.file, r.path, r.name, r.dir, r.remaining = f, f.Name(), m.Name, dir, m.Size
		r.updated = time.Now()
		file := r.file
		r.timer = time.AfterFunc(30*time.Second, func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.file != file {
				return
			}
			if age := time.Since(r.updated); age < 30*time.Second {
				r.timer.Reset(30*time.Second - age)
				return
			}
			r.cleanup()
			r.reply("error", "傳輸逾時")
		})
		r.reply("ready", "")
	case 1:
		if r.file == nil || len(b) > 32769 || int64(len(b)-1) > r.remaining {
			r.cleanup()
			r.reply("error", "傳輸資料無效")
			return
		}
		n, e := r.file.Write(b[1:])
		r.remaining -= int64(n)
		if e != nil {
			r.cleanup()
			r.reply("error", e.Error())
			return
		}
		r.updated = time.Now()
		r.timer.Reset(30 * time.Second)
	case 2:
		if r.file == nil || r.remaining != 0 {
			r.cleanup()
			r.reply("error", "檔案未完整接收")
			return
		}
		e := r.file.Sync()
		ce := r.file.Close()
		r.file = nil
		if e == nil {
			e = ce
		}
		if e == nil && !p.engine.host.Load().CanTransfer(c) {
			e = fmt.Errorf("授權已失效")
		}
		if e == nil {
			_, e = desktop.CommitUpload(r.path, r.dir, r.name)
		}
		os.Remove(r.path)
		r.cleanup()
		if e != nil {
			r.reply("error", e.Error())
		} else {
			r.reply("saved", "")
		}
	case 3:
		r.batchRemaining = 0
		r.batchCount = 0
		r.cleanup()
		r.reply("cancelled", "")
	}
}
func (e *Engine) SendFiles(sid string, paths []string) error {
	p := e.get(sid)
	if p == nil {
		return fmt.Errorf("連線已結束")
	}
	if len(paths) == 0 || len(paths) > 100 {
		return fmt.Errorf("每批最多 100 個檔案")
	}
	p.mu.Lock()
	if p.sending || p.closed {
		p.mu.Unlock()
		return fmt.Errorf("已有傳輸進行中")
	}
	d := p.file
	limit := p.remoteLimit
	if limit == 0 {
		limit = 1 << 30
	}
	p.mu.Unlock()
	if d == nil || d.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("檔案通道未就緒")
	}
	var total int64
	for _, path := range paths {
		s, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !s.Mode().IsRegular() || s.Size() > limit-total {
			return fmt.Errorf("超過每批傳輸上限")
		}
		total += s.Size()
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	transfer := hex.EncodeToString(nonce)
	p.mu.Lock()
	if p.sending || p.closed {
		p.mu.Unlock()
		return fmt.Errorf("傳輸進行中或連線已結束")
	}
	p.sending = true
	p.fileCancel = make(chan struct{})
	cancel := p.fileCancel
	p.mu.Unlock()
	go func() {
		result := "所有檔案已儲存"
		defer func() {
			p.mu.Lock()
			p.sending = false
			p.fileCancel = nil
			p.mu.Unlock()
			e.emit(Event{Kind: "file_status", Session: sid, Text: result})
		}()
		header, _ := json.Marshal(fileMeta{Type: "batch", Transfer: transfer, Count: len(paths), Total: total})
		if err := d.Send(append([]byte{0}, header...)); err != nil {
			result = "傳輸失敗：" + err.Error()
			return
		}
		if err := p.waitAck("batch_ready", transfer, cancel); err != nil {
			result = "傳輸失敗：" + err.Error()
			return
		}
		for _, path := range paths {
			err := p.sendFile(d, path, transfer, cancel)
			if err != nil {
				d.Send([]byte{3})
				result = "傳輸失敗：" + err.Error()
				return
			}
		}
	}()
	return nil
}
func (e *Engine) CancelFiles(sid string) {
	if p := e.get(sid); p != nil {
		p.mu.Lock()
		if p.sending && p.fileCancel != nil {
			close(p.fileCancel)
			p.fileCancel = nil
		}
		p.mu.Unlock()
	}
}
func (p *Peer) sendFile(d *webrtc.DataChannel, path, transfer string, cancel <-chan struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil {
		return err
	}
	// Do not mistake a late acknowledgement from a cancelled transfer for ready.
	for {
		select {
		case <-p.fileAck:
		default:
			goto drained
		}
	}
drained:
	b, _ := json.Marshal(fileMeta{Type: "begin", Transfer: transfer, Name: filepath.Base(path), Size: s.Size()})
	if err = d.Send(append([]byte{0}, b...)); err != nil {
		return err
	}
	if err = p.waitAck("ready", transfer, cancel); err != nil {
		return err
	}
	pace := time.NewTicker(time.Second / 64)
	defer pace.Stop()
	buf := make([]byte, 16385)
	buf[0] = 1
	var sent int64
	last := time.Now()
	for {
		select {
		case <-cancel:
			return fmt.Errorf("已取消")
		case <-p.engine.ctx.Done():
			return fmt.Errorf("已結束")
		default:
		}
		n, er := f.Read(buf[1:])
		if n > 0 {
			select {
			case <-pace.C:
			case <-cancel:
				return fmt.Errorf("已取消")
			case <-p.engine.ctx.Done():
				return fmt.Errorf("已結束")
			}
			for d.BufferedAmount() > 128*1024 {
				select {
				case <-cancel:
					return fmt.Errorf("已取消")
				case <-time.After(10 * time.Millisecond):
				}
				if d.ReadyState() != webrtc.DataChannelStateOpen {
					return fmt.Errorf("通道已關閉")
				}
			}
			if err = d.Send(buf[:n+1]); err != nil {
				return err
			}
			sent += int64(n)
			if time.Since(last) > 250*time.Millisecond {
				p.engine.emit(Event{Kind: "file_status", Session: p.sid, Text: fmt.Sprintf("%s：%d / %d bytes", s.Name(), sent, s.Size())})
				last = time.Now()
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return er
		}
	}
	if err = d.Send([]byte{2}); err != nil {
		return err
	}
	p.engine.emit(Event{Kind: "file_status", Session: p.sid, Text: s.Name() + "：等待主機儲存"})
	return p.waitAck("saved", transfer, cancel)
}
func (p *Peer) waitAck(kind, transfer string, cancel <-chan struct{}) error {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case m := <-p.fileAck:
			if m.Transfer != transfer {
				continue
			}
			if m.Type != kind {
				return fmt.Errorf("%s", m.Error)
			}
			return nil
		case <-cancel:
			return fmt.Errorf("已取消")
		case <-p.engine.ctx.Done():
			return fmt.Errorf("已結束")
		case <-timer.C:
			return fmt.Errorf("傳輸逾時")
		}
	}
}
