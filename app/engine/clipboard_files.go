package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"kvm/core/clipboard"
)

const clipboardFileFrame = 4
const clipboardChunkSize = 8 * 1024

type clipboardFileMessage struct {
	Request uint64
	Offer   string
	Item    int
	Offset  int64
	Length  int
	Data    []byte
	Reply   bool
	Failed  bool
}

type clipboardFiles struct {
	readMu        sync.Mutex
	mu            sync.Mutex
	offerID       string
	offerSequence uint32
	relay         func(int, int64, []byte) (int, error)
	offer         *clipboard.FileOffer
	allowed       func() bool
	next          uint64
	request       uint64
	reply         chan clipboardFileMessage
	serving       bool
}

func (p *Peer) sendClipboardFile(message clipboardFileMessage) error {
	p.mu.Lock()
	channel, closed := p.file, p.closed
	p.mu.Unlock()
	if closed || channel == nil || channel.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("剪貼簿檔案通道尚未就緒")
	}
	if channel.BufferedAmount() > 128*1024 {
		return fmt.Errorf("剪貼簿檔案通道忙碌")
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return channel.Send(append([]byte{clipboardFileFrame}, data...))
}

func (p *Peer) handleClipboardFile(data []byte) bool {
	if len(data) == 0 || data[0] != clipboardFileFrame {
		return false
	}
	var m clipboardFileMessage
	if len(data) > 16000 || json.Unmarshal(data[1:], &m) != nil {
		return true
	}
	f := &p.clipFiles
	f.mu.Lock()
	defer f.mu.Unlock()
	if m.Reply {
		if f.reply != nil && f.request == m.Request {
			select {
			case f.reply <- m:
			default:
			}
		}
		return true
	}
	if f.serving || m.Request == 0 || m.Length <= 0 || m.Length > clipboardChunkSize ||
		m.Offset < 0 || len(m.Data) != 0 {
		return true
	}
	f.serving = true
	offer, id, allowed, relay := f.offer, f.offerID, f.allowed, f.relay
	go func() {
		reply := clipboardFileMessage{Request: m.Request, Reply: true, Failed: true}
		if (offer != nil || relay != nil) && id == m.Offer && allowed != nil && allowed() {
			if relay != nil {
				buf := make([]byte, m.Length)
				if n, err := relay(m.Item, m.Offset, buf); err == nil && n == len(buf) {
					reply.Data = buf
				}
			} else {
				reply.Data = readOfferedChunk(offer, m)
			}
			f.mu.Lock()
			current := f.offer == offer && f.offerID == id
			f.mu.Unlock()
			if current && allowed() && reply.Data != nil {
				reply.Failed = false
			} else {
				reply.Data = nil
			}
		}
		f.mu.Lock()
		f.serving = false
		p.sendClipboardFile(reply)
		f.mu.Unlock()
	}()
	return true
}

func readOfferedChunk(offer *clipboard.FileOffer, m clipboardFileMessage) []byte {
	file, err := offer.Open(m.Item)
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || m.Offset > info.Size() || int64(m.Length) > info.Size()-m.Offset {
		return nil
	}
	data := make([]byte, m.Length)
	if _, err := file.ReadAt(data, m.Offset); err != nil {
		return nil
	}
	after, err := file.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil
	}
	return data
}

func (p *Peer) readClipboardFile(id string, item int, offset int64, dst []byte, allowed func() bool) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if !allowed() {
		return 0, fmt.Errorf("剪貼簿檔案讀取未授權")
	}
	f := &p.clipFiles
	f.readMu.Lock()
	defer f.readMu.Unlock()
	if !allowed() {
		return 0, fmt.Errorf("剪貼簿檔案讀取未授權")
	}
	f.mu.Lock()
	f.next++
	request := f.next
	reply := make(chan clipboardFileMessage, 1)
	f.request, f.reply = request, reply
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.reply = nil
		f.mu.Unlock()
	}()

	p.mu.Lock()
	startGeneration := p.relayGeneration
	p.mu.Unlock()
	length := min(len(dst), clipboardChunkSize)
	if err := p.sendClipboardFile(clipboardFileMessage{
		Request: request, Offer: id, Item: item, Offset: offset, Length: length,
	}); err != nil {
		return 0, err
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	extended := false
	for {
		select {
		case m := <-reply:
			if !allowed() || m.Failed || len(m.Data) != length {
				return 0, fmt.Errorf("剪貼簿檔案讀取失敗")
			}
			return copy(dst, m.Data), nil
		case <-timer.C:
			p.mu.Lock()
			recovering := !p.closed && (p.relayRecovering || p.relayGeneration != startGeneration)
			p.mu.Unlock()
			if recovering && !extended {
				extended = true
				timer.Reset(15 * time.Second)
				continue
			}
			return 0, fmt.Errorf("剪貼簿檔案讀取逾時")
		case <-poll.C:
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || !allowed() {
				return 0, io.ErrClosedPipe
			}
		case <-p.engine.ctx.Done():
			return 0, io.ErrClosedPipe
		}
	}
}

func (e *Engine) OfferClipboardFiles(sid string, paths []string, limit int64, allowed func() bool) (*clipboard.ClipboardManifest, error) {
	p := e.get(sid)
	if p == nil {
		return nil, io.ErrClosedPipe
	}
	if len(paths) == 0 {
		e.ClearClipboardFiles(sid)
		return nil, nil
	}
	offer, err := clipboard.NewFileOffer(paths, limit)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	m := &clipboard.ClipboardManifest{ID: hex.EncodeToString(nonce[:]), Items: offer.Items()}
	if err := m.Validate(limit); err != nil {
		return nil, err
	}
	f := &p.clipFiles
	f.mu.Lock()
	f.offerID, f.offer, f.allowed, f.offerSequence, f.relay = m.ID, offer, allowed, 0, nil
	f.mu.Unlock()
	return m, nil
}

func (e *Engine) ClearClipboardFiles(sid string) {
	if p := e.get(sid); p != nil {
		f := &p.clipFiles
		f.mu.Lock()
		f.offerID, f.offer, f.allowed, f.offerSequence, f.relay = "", nil, nil, 0, nil
		f.mu.Unlock()
	}
}

func (e *Engine) ReadClipboardFile(sid, offer string, item int, offset int64, data []byte, allowed func() bool) (int, error) {
	p := e.get(sid)
	if p == nil {
		return 0, io.ErrClosedPipe
	}
	return p.readClipboardFile(offer, item, offset, data, allowed)
}

type ClipboardPublisher func([]clipboard.FileItem, clipboard.FormatBatch, uint32, func(int, int64, []byte) (int, error)) error

// SetClipboardPublisher installs the App's UI-thread OLE publisher before Start.
func (e *Engine) SetClipboardPublisher(publisher ClipboardPublisher) {
	e.clipboardPublisher = publisher
}

func (p *Peer) CaptureNativeClipboard(limit int64, allowed func() bool) (clipboard.Snapshot, uint32, error) {
	return p.engine.CaptureClipboard(p.sid, limit, allowed)
}

func (e *Engine) CaptureClipboard(sid string, limit int64, allowed func() bool) (clipboard.Snapshot, uint32, error) {
	before := clipboard.GetSequenceNumber()
	batch, seq, err := clipboard.ReadBatch()
	if err != nil {
		return clipboard.Snapshot{}, 0, err
	}
	paths, err := clipboard.ReadFileDrop()
	if err != nil {
		return clipboard.Snapshot{}, 0, err
	}
	if before != seq || seq != clipboard.GetSequenceNumber() {
		return clipboard.Snapshot{}, 0, fmt.Errorf("剪貼簿內容已變更")
	}
	p := e.get(sid)
	if p == nil {
		return clipboard.Snapshot{}, 0, io.ErrClosedPipe
	}
	f := &p.clipFiles
	f.mu.Lock()
	existing, existingID, previousGrant := f.offer, f.offerID, f.allowed
	sameSelection := f.offerSequence == seq && existing != nil
	f.mu.Unlock()
	if sameSelection && previousGrant != nil && previousGrant() {
		m := &clipboard.ClipboardManifest{ID: existingID, Items: existing.Items()}
		if err := m.Validate(limit); err == nil {
			return clipboard.Snapshot{Batch: batch, Files: m}, seq, nil
		}
	}
	offer, err := e.OfferClipboardFiles(sid, paths, limit, func() bool {
		return allowed() && clipboard.GetSequenceNumber() == seq
	})
	if err != nil {
		return clipboard.Snapshot{}, 0, err
	}
	if offer != nil {
		f.mu.Lock()
		if f.offerID == offer.ID {
			f.offerSequence = seq
		}
		f.mu.Unlock()
	}
	return clipboard.Snapshot{Batch: batch, Files: offer}, seq, nil
}

func (p *Peer) InstallNativeClipboard(snapshot clipboard.Snapshot, expected uint32, allowed func() bool) (uint32, error) {
	if snapshot.Files == nil {
		if !allowed() {
			return 0, fmt.Errorf("剪貼簿未授權")
		}
		return clipboard.WriteBatch(snapshot.Batch, expected)
	}
	e := p.engine
	if e.clipboardPublisher == nil {
		return 0, fmt.Errorf("檔案剪貼簿無法使用")
	}
	type result struct {
		sequence uint32
		err      error
	}
	done := make(chan result, 1)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	deadline := time.Now().Add(2 * time.Second)
	e.emit(Event{Kind: "clipboard_install", Run: func() {
		if time.Now().After(deadline) || !allowed() {
			done <- result{err: fmt.Errorf("剪貼簿未授權或已逾時")}
			return
		}
		err := e.clipboardPublisher(snapshot.Files.Items, snapshot.Batch, expected, func(item int, offset int64, data []byte) (int, error) {
			return p.readClipboardFile(snapshot.Files.ID, item, offset, data, allowed)
		})
		done <- result{sequence: clipboard.GetSequenceNumber(), err: err}
	}})
	select {
	case response := <-done:
		return response.sequence, response.err
	case <-timer.C:
		return 0, fmt.Errorf("剪貼簿更新逾時")
	case <-e.ctx.Done():
		return 0, io.ErrClosedPipe
	}
}

func (e *Engine) ClipboardLimit(sid string) int64 {
	p := e.get(sid)
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.remoteLimit
}

func (p *Peer) ReadNativeClipboardFile(id string, item int, offset int64, data []byte, allowed func() bool) (int, error) {
	return p.readClipboardFile(id, item, offset, data, allowed)
}

func (p *Peer) RelayNativeClipboardFiles(manifest *clipboard.ClipboardManifest, allowed func() bool, read func(int, int64, []byte) (int, error)) {
	f := &p.clipFiles
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offer, f.offerSequence = nil, 0
	f.offerID, f.allowed, f.relay = "", nil, nil
	if manifest != nil {
		f.offerID, f.allowed, f.relay = manifest.ID, allowed, read
	}
}
