package engine

import (
	"fmt"
	"github.com/pion/webrtc/v4"
	"kvm/core/streamtrace"
	"sync"
	"time"
)

const bulkChunkSize = 16 * 1024
const bulkMaxBuffered = 8 * 1024 * 1024

type bulkSender struct {
	once  sync.Once
	mu    sync.Mutex
	wake  chan struct{}
	done  chan struct{}
	close sync.Once
}

func (b *bulkSender) init() {
	b.once.Do(func() { b.wake = make(chan struct{}, 1); b.done = make(chan struct{}) })
}
func (b *bulkSender) signal() {
	b.init()
	select {
	case b.wake <- struct{}{}:
	default:
	}
}
func (b *bulkSender) stop() { b.init(); b.close.Do(func() { close(b.done) }); b.signal() }
func (p *Peer) setupFileChannel(d *webrtc.DataChannel) {
	p.bulk.init()
	d.SetBufferedAmountLowThreshold(256 * 1024)
	d.OnBufferedAmountLow(p.bulk.signal)
}

// One producer at a time admits bounded messages to SCTP. The transport gate
// separately gives SRTP precedence over already admitted DTLS datagrams.
func (p *Peer) sendBulk(data []byte, cancel <-chan struct{}, valid func() bool) error {
	p.bulk.init()
	p.bulk.mu.Lock()
	defer p.bulk.mu.Unlock()
	p.mu.Lock()
	d, closed := p.file, p.closed
	p.mu.Unlock()
	if closed || d == nil {
		return fmt.Errorf("檔案通道已關閉")
	}
	start := time.Now()
	timer := time.NewTicker(5 * time.Millisecond)
	defer timer.Stop()
	for {
		if valid != nil && !valid() {
			return fmt.Errorf("檔案授權或選取已變更")
		}
		select {
		case <-cancel:
			return fmt.Errorf("已取消")
		case <-p.bulk.done:
			return fmt.Errorf("已結束")
		case <-p.engine.ctx.Done():
			return fmt.Errorf("已結束")
		default:
		}
		if d.ReadyState() != webrtc.DataChannelStateOpen {
			return fmt.Errorf("檔案通道已關閉")
		}
		ceiling := uint64(bulkMaxBuffered)
		if p.relayBudgetMbps.Load() > 0 {
			ceiling = min(ceiling, uint64(max(64*1024, p.relayBudgetMbps.Load()*1000000/8/4)))
		}
		p.mu.Lock()
		control := p.control
		p.mu.Unlock()
		controlsBusy := control != nil && control.BufferedAmount() > 16*1024
		if p.videoBusy.Load() == 0 && time.Now().UnixNano() >= p.bulkHold.Load() && !controlsBusy && d.BufferedAmount()+uint64(len(data)) <= ceiling {
			if valid != nil && !valid() {
				return fmt.Errorf("檔案授權或選取已變更")
			}
			err := d.Send(data)
			streamtrace.Emit(streamtrace.Event{Stage: "file.send_wait", Object: p.traceObject(), DurationUS: time.Since(start).Microseconds(), Value: int64(len(data)), Result: int64(d.BufferedAmount())})
			return err
		}
		select {
		case <-cancel:
			return fmt.Errorf("已取消")
		case <-p.bulk.done:
			return fmt.Errorf("已結束")
		case <-p.engine.ctx.Done():
			return fmt.Errorf("已結束")
		case <-p.bulk.wake:
		case <-timer.C:
		}
	}
}
func (p *Peer) deferBulk(delay time.Duration) {
	until := time.Now().Add(delay).UnixNano()
	for {
		old := p.bulkHold.Load()
		if old >= until || p.bulkHold.CompareAndSwap(old, until) {
			break
		}
	}
	p.bulk.signal()
}
func (p *Peer) setFileMeta(d *webrtc.DataChannel) {
	p.mu.Lock()
	p.fileMetaChannel = d
	p.mu.Unlock()
	d.OnMessage(func(m webrtc.DataChannelMessage) {
		if p.handleClipboardFile(m.Data) {
			return
		}
		p.acceptFileAck(m.Data)
	})
}
func (p *Peer) sendFileControl(data []byte) error {
	p.mu.Lock()
	d, closed := p.fileMetaChannel, p.closed
	p.mu.Unlock()
	if closed || d == nil || d.ReadyState() != webrtc.DataChannelStateOpen {
		return fmt.Errorf("檔案控制通道未就緒，兩端需使用新版")
	}
	if d.BufferedAmount() > 64*1024 {
		return fmt.Errorf("檔案控制通道壅塞")
	}
	return d.Send(data)
}
