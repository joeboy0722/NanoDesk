package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
)

const (
	Version    = 1
	HeaderSize = 40
	TagSize    = 16
	MaxPayload = 1400
)

type Kind byte

const (
	Challenge      Kind = 1
	ChallengeReply Kind = 2
	Data           Kind = 3
	Heartbeat      Kind = 4
	HeartbeatReply Kind = 5
)

type ChannelID [16]byte
type Key [32]byte

type Packet struct {
	Kind       Kind
	Channel    ChannelID
	Generation uint64
	Sequence   uint64
	Payload    []byte
}

func (p Packet) Marshal(key Key) ([]byte, error) {
	if p.Kind < Challenge || p.Kind > HeartbeatReply || p.Generation == 0 || p.Sequence == 0 || len(p.Payload) > MaxPayload {
		return nil, errors.New("invalid relay packet")
	}
	b := make([]byte, HeaderSize+len(p.Payload)+TagSize)
	copy(b, []byte{'N', 'R', Version, byte(p.Kind)})
	copy(b[4:20], p.Channel[:])
	binary.BigEndian.PutUint64(b[20:28], p.Generation)
	binary.BigEndian.PutUint64(b[28:36], p.Sequence)
	binary.BigEndian.PutUint16(b[36:38], uint16(len(p.Payload)))
	copy(b[HeaderSize:], p.Payload)
	mac := hmac.New(sha256.New, key[:])
	mac.Write(b[:len(b)-TagSize])
	copy(b[len(b)-TagSize:], mac.Sum(nil)[:TagSize])
	return b, nil
}

// Decode authenticates the entire datagram before exposing its contents.
// The returned payload references b and is valid until its buffer is reused.
func Decode(b []byte, key Key) (Packet, error) {
	var p Packet
	if len(b) < HeaderSize+TagSize || len(b) > HeaderSize+MaxPayload+TagSize ||
		b[0] != 'N' || b[1] != 'R' || b[2] != Version ||
		b[3] < byte(Challenge) || b[3] > byte(HeartbeatReply) ||
		b[38] != 0 || b[39] != 0 ||
		int(binary.BigEndian.Uint16(b[36:38])) != len(b)-HeaderSize-TagSize {
		return p, errors.New("invalid relay datagram")
	}
	mac := hmac.New(sha256.New, key[:])
	mac.Write(b[:len(b)-TagSize])
	if !hmac.Equal(b[len(b)-TagSize:], mac.Sum(nil)[:TagSize]) {
		return p, errors.New("relay authentication failed")
	}
	p.Kind = Kind(b[3])
	copy(p.Channel[:], b[4:20])
	p.Generation = binary.BigEndian.Uint64(b[20:28])
	p.Sequence = binary.BigEndian.Uint64(b[28:36])
	if p.Generation == 0 || p.Sequence == 0 {
		return Packet{}, errors.New("invalid relay sequence")
	}
	p.Payload = b[HeaderSize : len(b)-TagSize]
	return p, nil
}

// ReplayWindow accepts reordered packets within a 64-packet window.
// Call only after authentication and checking the channel and generation.
// Each receiving direction and path generation owns a separate window.
type ReplayWindow struct {
	mu      sync.Mutex
	highest uint64
	seen    uint64
}

func (w *ReplayWindow) Accept(sequence uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sequence == 0 {
		return false
	}
	if sequence > w.highest {
		shift := sequence - w.highest
		w.seen = w.seen<<shift | 1
		w.highest = sequence
		return true
	}
	distance := w.highest - sequence
	if distance >= 64 || w.seen&(uint64(1)<<distance) != 0 {
		return false
	}
	w.seen |= uint64(1) << distance
	return true
}
