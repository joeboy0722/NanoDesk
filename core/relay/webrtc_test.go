package relay

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/webrtc/v4"
)

func TestWebRTCThroughSharedRelay(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewEndpoint(socket)
	defer endpoint.Close()
	verifySharedWebRTCPair(t, endpoint, socket, 0, true)
}

func verifySharedWebRTCPair(t *testing.T, endpoint *Endpoint, socket net.PacketConn, index int, recoverPath bool) func() {
	t.Helper()
	a, err := newCredentials(1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newCredentials(1)
	if err != nil {
		t.Fatal(err)
	}
	if err := endpoint.AddPair(a, b); err != nil {
		t.Fatal(err)
	}
	addressA := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	addressB := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	var switches []*SwitchConn
	makePeer := func(credentials RouteCredentials, local, remote *net.UDPAddr) *webrtc.PeerConnection {
		t.Helper()
		conn, err := DialUDP("udp4", socket.LocalAddr().String(), remote, credentials)
		if err != nil {
			t.Fatal(err)
		}
		stable := NewSwitchConn(conn, local, nil)
		switches = append(switches, stable)
		mux := ice.NewUDPMuxDefault(ice.UDPMuxParams{UDPConn: stable})
		t.Cleanup(func() { mux.Close() })
		var settings webrtc.SettingEngine
		settings.SetICEUDPMux(mux)
		settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
		settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
		api := webrtc.NewAPI(webrtc.WithSettingEngine(settings))
		pc, err := api.NewPeerConnection(webrtc.Configuration{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		return pc
	}
	left, right := makePeer(a, addressA, addressB), makePeer(b, addressB, addressA)
	done := make(chan string, 1)
	right.OnDataChannel(func(d *webrtc.DataChannel) {
		d.OnMessage(func(m webrtc.DataChannelMessage) { done <- string(m.Data) })
	})
	channel, err := left.CreateDataChannel("relay-verification", nil)
	if err != nil {
		t.Fatal(err)
	}
	channel.OnOpen(func() { channel.SendText("WebRTC through shared UDP relay") })
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	setLocal := func(pc *webrtc.PeerConnection, sdp webrtc.SessionDescription) {
		t.Helper()
		ready := webrtc.GatheringCompletePromise(pc)
		check(pc.SetLocalDescription(sdp))
		select {
		case <-ready:
		case <-time.After(5 * time.Second):
			t.Fatal("ICE gathering timeout")
		}
	}
	offer, err := left.CreateOffer(nil)
	check(err)
	setLocal(left, offer)
	check(right.SetRemoteDescription(*left.LocalDescription()))
	answer, err := right.CreateAnswer(nil)
	check(err)
	setLocal(right, answer)
	check(left.SetRemoteDescription(*right.LocalDescription()))
	select {
	case message := <-done:
		if message != "WebRTC through shared UDP relay" {
			t.Fatal(message)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("WebRTC relay timed out: left=%s right=%s", left.ConnectionState(), right.ConnectionState())
	}
	if !recoverPath {
		return func() {
			t.Helper()
			payload := fmt.Sprintf("session %d still isolated after all twenty connected", index)
			check(channel.SendText(payload))
			select {
			case message := <-done:
				if message != payload {
					t.Fatalf("session %d received another session's message %q", index, message)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("session %d stopped forwarding", index)
			}
		}
	}
	// Change the physical server socket and both path keys without replacing
	// either PeerConnection or its established SCTP data channel.
	nextSocket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	check(err)
	next := NewEndpoint(nextSocket)
	defer next.Close()
	a.Generation, b.Generation = 2, 2
	a.ReceiveKey, a.SendKey = Key{11}, Key{12}
	b.ReceiveKey, b.SendKey = Key{13}, Key{14}
	check(next.AddPair(a, b))
	endpoint.Close()
	nextLeft, err := DialUDP("udp4", nextSocket.LocalAddr().String(), addressB, a)
	check(err)
	nextRight, err := DialUDP("udp4", nextSocket.LocalAddr().String(), addressA, b)
	check(err)
	check(switches[0].Replace(nextLeft))
	check(switches[1].Replace(nextRight))
	check(channel.SendText("same WebRTC session after route replacement"))
	select {
	case message := <-done:
		if message != "same WebRTC session after route replacement" {
			t.Fatal(message)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("existing WebRTC data channel did not survive route replacement")
	}

	return nil

}

func TestTwentyConcurrentWebRTCSessionsOnOnePort(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewEndpoint(socket)
	t.Cleanup(func() { endpoint.Close() })
	var verify []func()
	for i := 0; i < 20; i++ {
		verify = append(verify, verifySharedWebRTCPair(t, endpoint, socket, i, false))
	}
	for _, check := range verify {
		check()
	}
}
