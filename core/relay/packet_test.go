package relay

import (
	"bytes"
	"testing"
)

func TestPacketAuthenticationAndReplay(t *testing.T) {
	key := Key{1, 2, 3}
	p := Packet{Kind: Data, Channel: ChannelID{4}, Generation: 1, Sequence: 7, Payload: []byte("encrypted WebRTC")}
	b, err := p.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b, key)
	if err != nil || got.Channel != p.Channel || got.Sequence != p.Sequence || !bytes.Equal(got.Payload, p.Payload) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	for i := range b {
		tampered := append([]byte(nil), b...)
		tampered[i] ^= 1
		if _, err := Decode(tampered, key); err == nil {
			t.Fatalf("accepted modification at byte %d", i)
		}
	}
	if _, err := Decode(b, Key{9}); err == nil {
		t.Fatal("accepted another channel key")
	}
	var window ReplayWindow
	for _, n := range []uint64{7, 9, 8, 70} {
		if !window.Accept(n) {
			t.Fatalf("rejected fresh packet %d", n)
		}
	}
	for _, n := range []uint64{0, 7, 8, 9, 70, 6} {
		if window.Accept(n) {
			t.Fatalf("accepted replay or stale packet %d", n)
		}
	}
}
