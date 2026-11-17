package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"kvm/core/clipboard"
	"sync"
	"time"
)

const clipboardFileFrame = 4
const clipboardDataFrame = 5
const clipboardInitialWindow = 256 * 1024
const clipboardMaxWindow = 8 * 1024 * 1024

type clipboardFileMessage struct {
	Version int
	Request uint64
	Offer   string
	Item    int
	Offset  int64
	Length  int
	Cancel  bool
	Failed  bool
}

type clipboardFiles struct {
	readMu        sync.Mutex
	mu            sync.Mutex
	offerID       string
	offerSequence uint32
	relay         func(int, int64, []byte) (int, error)
	relayItems    []clipboard.FileItem
	offer         *clipboard.FileOffer
	allowed       func() bool
	next          uint64
	epoch         uint64
	jobs          map[uint64]*clipboardJob
	queue         []*clipboardJob
	serving       bool
	windows       []*clipboardWindow
	readOffer     string
	readItem      int
	readNext      int64
	readLast      time.Time
}

func (p *Peer) sendClipboardFile(m clipboardFileMessage) error {
	m.Version = 2
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return p.sendFileControl(append([]byte{clipboardFileFrame}, data...))
}
func (p *Peer) handleClipboardFile(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if data[0] == clipboardDataFrame {
		p.receiveClipboardData(data)
		return true
	}
	if data[0] != clipboardFileFrame {
		return false
	}
	var m clipboardFileMessage
	if len(data) > 4096 || json.Unmarshal(data[1:], &m) != nil || m.Version != 2 || m.Request == 0 {
		return true
	}
	f := &p.clipFiles
	if m.Failed {
		f.mu.Lock()
		for _, w := range f.windows {
			if w.id == m.Request {
				w.fail(fmt.Errorf("剪貼簿來源已變更或未授權"))
			}
		}
		f.mu.Unlock()
		return true
	}
	if m.Cancel {
		f.mu.Lock()
		if j := f.jobs[m.Request]; j != nil {
			j.stop()
		}
		f.mu.Unlock()
		return true
	}
	if m.Length <= 0 || m.Length > clipboardMaxWindow || m.Offset < 0 || m.Item < 0 || len(m.Offer) != 32 {
		return true
	}
	f.mu.Lock()
	if len(f.jobs) >= 3 {
		f.mu.Unlock()
		return true
	}
	if _, ok := f.jobs[m.Request]; ok {
		f.mu.Unlock()
		return true
	}
	if f.jobs == nil {
		f.jobs = map[uint64]*clipboardJob{}
	}
	j := &clipboardJob{message: m, epoch: f.epoch, offer: f.offer, relay: f.relay, items: append([]clipboard.FileItem(nil), f.relayItems...), allowed: f.allowed, done: make(chan struct{})}
	f.jobs[m.Request] = j
	f.queue = append(f.queue, j)
	if !f.serving {
		f.serving = true
		go p.serveClipboardJobs()
	}
	f.mu.Unlock()
	return true
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
	f.invalidateOfferLocked()
	f.relayItems = nil
	f.offerID, f.offer, f.allowed, f.offerSequence, f.relay = m.ID, offer, allowed, 0, nil
	f.mu.Unlock()
	return m, nil
}

func (e *Engine) ClearClipboardFiles(sid string) {
	if p := e.get(sid); p != nil {
		f := &p.clipFiles
		f.mu.Lock()
		f.invalidateOfferLocked()
		f.relayItems = nil
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
	f.invalidateOfferLocked()
	f.relayItems = nil
	f.offer, f.offerSequence = nil, 0
	f.offerID, f.allowed, f.relay = "", nil, nil
	if manifest != nil {
		f.offerID, f.allowed, f.relay = manifest.ID, allowed, read
		f.relayItems = append([]clipboard.FileItem(nil), manifest.Items...)
	}
}
