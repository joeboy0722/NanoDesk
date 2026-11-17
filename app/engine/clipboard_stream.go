package engine

import (
	"encoding/binary"
	"fmt"
	"io"
	"kvm/core/clipboard"
	"kvm/core/streamtrace"
	"os"
	"sync"
	"time"
)

const clipboardDataHeader = 26
const clipboardEnd = 1

type clipboardJob struct {
	message clipboardFileMessage
	epoch   uint64
	offer   *clipboard.FileOffer
	relay   func(int, int64, []byte) (int, error)
	items   []clipboard.FileItem
	allowed func() bool
	done    chan struct{}
	once    sync.Once
}

func (j *clipboardJob) stop() { j.once.Do(func() { close(j.done) }) }
func (f *clipboardFiles) invalidateOfferLocked() {
	f.epoch++
	for _, j := range f.jobs {
		j.stop()
	}
	f.queue = nil
	f.jobs = map[uint64]*clipboardJob{}
}
func (p *Peer) serveClipboardJobs() {
	f := &p.clipFiles
	for {
		f.mu.Lock()
		if len(f.queue) == 0 {
			f.serving = false
			f.mu.Unlock()
			return
		}
		j := f.queue[0]
		f.queue = f.queue[1:]
		f.mu.Unlock()
		err := p.serveClipboardWindow(j)
		f.mu.Lock()
		delete(f.jobs, j.message.Request)
		f.mu.Unlock()
		if err != nil {
			_ = p.sendClipboardFile(clipboardFileMessage{Request: j.message.Request, Failed: true})
		}
	}
}
func (p *Peer) clipboardJobValid(j *clipboardJob) bool {
	select {
	case <-j.done:
		return false
	default:
	}
	f := &p.clipFiles
	f.mu.Lock()
	same := f.epoch == j.epoch && f.offerID == j.message.Offer && f.offer == j.offer
	f.mu.Unlock()
	return same && j.allowed != nil && j.allowed()
}
func (p *Peer) serveClipboardWindow(j *clipboardJob) error {
	if !p.clipboardJobValid(j) {
		return io.ErrClosedPipe
	}
	var file *os.File
	var size int64
	var initial os.FileInfo
	if j.offer != nil {
		var err error
		file, err = j.offer.Open(j.message.Item)
		if err != nil {
			return err
		}
		defer file.Close()
		initial, err = file.Stat()
		if err != nil {
			return err
		}
		size = initial.Size()
	} else if j.relay != nil && j.message.Item < len(j.items) && !j.items[j.message.Item].Directory {
		size = j.items[j.message.Item].Size
	} else {
		return io.ErrClosedPipe
	}
	if j.message.Offset > size {
		return io.ErrUnexpectedEOF
	}
	end := j.message.Offset + min(int64(j.message.Length), size-j.message.Offset)
	frame := make([]byte, clipboardDataHeader+bulkChunkSize)
	frame[0] = clipboardDataFrame
	binary.BigEndian.PutUint64(frame[1:9], j.message.Request)
	binary.BigEndian.PutUint64(frame[17:25], uint64(size))
	for offset := j.message.Offset; ; {
		if !p.clipboardJobValid(j) {
			return io.ErrClosedPipe
		}
		n := int(min(int64(bulkChunkSize), end-offset))
		dst := frame[clipboardDataHeader : clipboardDataHeader+n]
		if file != nil {
			if n > 0 {
				if _, err := file.ReadAt(dst, offset); err != nil {
					return err
				}
			}
			after, err := file.Stat()
			if err != nil || after.Size() != initial.Size() || !after.ModTime().Equal(initial.ModTime()) {
				return fmt.Errorf("剪貼簿檔案已變更")
			}
		} else {
			for done := 0; done < n; {
				read, err := j.relay(j.message.Item, offset+int64(done), dst[done:])
				if err != nil {
					return err
				}
				if read <= 0 || read > n-done {
					return io.ErrUnexpectedEOF
				}
				done += read
			}
		}
		binary.BigEndian.PutUint64(frame[9:17], uint64(offset))
		frame[25] = 0
		if offset+int64(n) == end {
			frame[25] = clipboardEnd
		}
		if err := p.sendBulk(frame[:clipboardDataHeader+n], j.done, func() bool { return p.clipboardJobValid(j) }); err != nil {
			return err
		}
		offset += int64(n)
		if offset == end {
			return nil
		}
	}
}

type clipboardWindow struct {
	mu       sync.Mutex
	id       uint64
	offer    string
	item     int
	start    int64
	limit    int
	data     []byte
	received int
	total    int64
	done     bool
	err      error
	wake     chan struct{}
}

func (w *clipboardWindow) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *clipboardWindow) fail(err error) {
	w.mu.Lock()
	w.err = err
	w.done = true
	w.mu.Unlock()
	w.signal()
}
func (p *Peer) receiveClipboardData(frame []byte) {
	if len(frame) < clipboardDataHeader || len(frame) > clipboardDataHeader+bulkChunkSize {
		return
	}
	id := binary.BigEndian.Uint64(frame[1:9])
	offset := int64(binary.BigEndian.Uint64(frame[9:17]))
	total := int64(binary.BigEndian.Uint64(frame[17:25]))
	flags := frame[25]
	payload := frame[clipboardDataHeader:]
	f := &p.clipFiles
	f.mu.Lock()
	var w *clipboardWindow
	for _, candidate := range f.windows {
		if candidate.id == id {
			w = candidate
			break
		}
	}
	f.mu.Unlock()
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.done {
		return
	}
	if offset < 0 || total < 0 || flags & ^byte(clipboardEnd) != 0 || offset != w.start+int64(w.received) || len(payload) > w.limit-w.received || offset > total || int64(len(payload)) > total-offset || (w.total >= 0 && w.total != total) {
		w.err = fmt.Errorf("剪貼簿串流資料無效")
		w.done = true
		w.signal()
		return
	}
	if w.data == nil {
		w.data = make([]byte, w.limit)
	}
	copy(w.data[w.received:], payload)
	w.received += len(payload)
	w.total = total
	if flags&clipboardEnd != 0 {
		w.done = true
		if offset+int64(len(payload)) != min(w.start+int64(w.limit), total) {
			w.err = io.ErrUnexpectedEOF
		}
	}
	w.signal()
}
func (p *Peer) newClipboardWindowLocked(id string, item int, start int64, size int) *clipboardWindow {
	f := &p.clipFiles
	f.next++
	w := &clipboardWindow{id: f.next, offer: id, item: item, start: start, limit: size, total: -1, wake: make(chan struct{}, 1)}
	f.windows = append(f.windows, w)
	return w
}
func (p *Peer) requestClipboardWindow(w *clipboardWindow) error {
	return p.sendClipboardFile(clipboardFileMessage{Request: w.id, Offer: w.offer, Item: w.item, Offset: w.start, Length: w.limit})
}
func (p *Peer) stopClipboardReads() {
	f := &p.clipFiles
	f.mu.Lock()
	for _, w := range f.windows {
		w.fail(io.ErrClosedPipe)
	}
	f.windows = nil
	f.invalidateOfferLocked()
	f.mu.Unlock()
}
func (p *Peer) cancelClipboardWindows() {
	f := &p.clipFiles
	f.mu.Lock()
	windows := f.windows
	f.windows = nil
	f.mu.Unlock()
	for _, w := range windows {
		w.fail(io.ErrClosedPipe)
		_ = p.sendClipboardFile(clipboardFileMessage{Request: w.id, Cancel: true})
	}
}
func (p *Peer) readClipboardFile(id string, item int, offset int64, dst []byte, allowed func() bool) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if allowed == nil || !allowed() || offset < 0 {
		p.cancelClipboardWindows()
		return 0, fmt.Errorf("剪貼簿檔案讀取未授權")
	}
	f := &p.clipFiles
	f.readMu.Lock()
	defer f.readMu.Unlock()
	f.mu.Lock()
	reset := f.readOffer != id || f.readItem != item || offset < f.readNext || (!f.readLast.IsZero() && time.Since(f.readLast) > 2*time.Second)
	var cancelled []*clipboardWindow
	if reset {
		cancelled = f.windows
		f.windows = nil
		f.readOffer = id
		f.readItem = item
	}
	// Discard fully consumed windows but keep a current window and at most one
	// prefetched successor. A seek backward starts a new source-authorized read.
	retained := f.windows[:0]
	for _, candidate := range f.windows {
		if offset < candidate.start+int64(candidate.limit) {
			retained = append(retained, candidate)
		}
	}
	f.windows = retained
	var w *clipboardWindow
	for _, candidate := range f.windows {
		if offset >= candidate.start && offset < candidate.start+int64(candidate.limit) {
			w = candidate
			break
		}
	}
	var request bool
	if w == nil {
		cancelled = append(cancelled, f.windows...)
		f.windows = nil
		w = p.newClipboardWindowLocked(id, item, offset, clipboardInitialWindow)
		request = true
	}
	f.mu.Unlock()
	for _, old := range cancelled {
		_ = p.sendClipboardFile(clipboardFileMessage{Request: old.id, Cancel: true})
	}
	if request {
		if err := p.requestClipboardWindow(w); err != nil {
			w.fail(err)
		}
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	for {
		if !allowed() {
			p.cancelClipboardWindows()
			return 0, fmt.Errorf("剪貼簿檔案讀取未授權")
		}
		w.mu.Lock()
		err := w.err
		available := w.received - int(offset-w.start)
		done, total := w.done, w.total
		if err == nil && available > 0 {
			n := min(len(dst), available)
			copy(dst, w.data[int(offset-w.start):int(offset-w.start)+n])
			w.mu.Unlock()
			f.mu.Lock()
			f.readNext = offset + int64(n)
			f.readLast = time.Now()
			var prefetch *clipboardWindow
			if len(f.windows) == 1 && (total < 0 || w.start+int64(w.limit) < total) {
				prefetch = p.newClipboardWindowLocked(id, item, w.start+int64(w.limit), min(clipboardMaxWindow, w.limit*2))
			}
			f.mu.Unlock()
			if prefetch != nil {
				if e := p.requestClipboardWindow(prefetch); e != nil {
					prefetch.fail(e)
				}
			}
			streamtrace.Emit(streamtrace.Event{Stage: "file.clipboard_read", Object: p.traceObject(), Value: int64(n)})
			return n, nil
		}
		wake := w.wake
		w.mu.Unlock()
		if err != nil {
			return 0, err
		}
		if done {
			if total == offset {
				return 0, io.EOF
			}
			return 0, io.ErrUnexpectedEOF
		}
		select {
		case <-wake:
		case <-poll.C:
		case <-timer.C:
			_ = p.sendClipboardFile(clipboardFileMessage{Request: w.id, Cancel: true})
			return 0, fmt.Errorf("剪貼簿串流無進展逾時")
		case <-p.engine.ctx.Done():
			return 0, io.ErrClosedPipe
		case <-p.bulk.done:
			return 0, io.ErrClosedPipe
		}
	}
}
