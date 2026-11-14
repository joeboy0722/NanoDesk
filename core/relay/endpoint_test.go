package relay

import (
	"net"
	"testing"
	"time"
)

func TestEndpointTwentyPairsShareOnePort(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewEndpoint(server)
	defer endpoint.Close()
	type client struct {
		conn        net.PacketConn
		credentials RouteCredentials
		seq         uint64
	}
	makeClient := func(id byte) *client {
		conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return &client{conn: conn, credentials: RouteCredentials{Channel: ChannelID{id}, ReceiveKey: Key{id, 1}, SendKey: Key{id, 2}, Generation: 1}}
	}
	send := func(c *client, kind Kind, payload []byte) {
		c.seq++
		b, err := (Packet{Kind: kind, Channel: c.credentials.Channel, Generation: 1, Sequence: c.seq, Payload: payload}).Marshal(c.credentials.ReceiveKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.conn.WriteTo(b, server.LocalAddr()); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *client) Packet {
		c.conn.SetReadDeadline(time.Now().Add(time.Second))
		b := make([]byte, 2048)
		n, _, err := c.conn.ReadFrom(b)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Decode(b[:n], c.credentials.SendKey)
		if err != nil || p.Channel != c.credentials.Channel {
			t.Fatalf("cross-channel packet: %v", err)
		}
		return p
	}
	pairs := make([][2]*client, 20)
	for i := range pairs {
		a, b := makeClient(byte(i*2+1)), makeClient(byte(i*2+2))
		if err := endpoint.AddPair(a.credentials, b.credentials); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*client{a, b} {
			send(c, Challenge, nil)
			challenge := read(c)
			if challenge.Kind != ChallengeReply {
				t.Fatal("missing challenge")
			}
			send(c, ChallengeReply, challenge.Payload)
			if read(c).Kind != HeartbeatReply {
				t.Fatal("address not confirmed")
			}
		}
		pairs[i] = [2]*client{a, b}
	}
	for i, pair := range pairs {
		payload := []byte{byte(i), 7, 9}
		send(pair[0], Data, payload)
		got := read(pair[1])
		if got.Kind != Data || string(got.Payload) != string(payload) {
			t.Fatal("incorrect forwarding")
		}
		send(pair[1], Data, payload)
		if string(read(pair[0]).Payload) != string(payload) {
			t.Fatal("reverse forwarding failed")
		}
	}
}

func TestClientPacketConnRoundTrip(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := NewEndpoint(socket)
	defer server.Close()
	a := RouteCredentials{Channel: ChannelID{1}, ReceiveKey: Key{1}, SendKey: Key{2}, Generation: 1}
	b := RouteCredentials{Channel: ChannelID{2}, ReceiveKey: Key{3}, SendKey: Key{4}, Generation: 1}
	if err := server.AddPair(a, b); err != nil {
		t.Fatal(err)
	}
	peerA := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	peerB := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	left, err := DialUDP("udp4", socket.LocalAddr().String(), peerB, a)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := DialUDP("udp4", socket.LocalAddr().String(), peerA, b)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	payload := []byte("WebRTC datagram")
	if _, err := left.WriteTo(payload, peerB); err != nil {
		t.Fatal(err)
	}
	right.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 2048)
	n, address, err := right.ReadFrom(buffer)
	if err != nil || string(buffer[:n]) != string(payload) || address.String() != peerA.String() {
		t.Fatalf("forwarded data: %q %v %v", buffer[:n], address, err)
	}
	if _, err := left.WriteTo(payload, peerA); err == nil {
		t.Fatal("accepted unrelated destination")
	}
}

func TestCrossEndpointForwardingAndRevocation(t *testing.T) {
	testCrossEndpointForwarding(t, "udp4", "127.0.0.1:0")
}

func TestIPv4ToIPv6RelayForwarding(t *testing.T) {
	testCrossEndpointForwarding(t, "udp6", "[::1]:0")
}

func testCrossEndpointForwarding(t *testing.T, rightNetwork, rightBind string) {
	socket := func(network, address string) net.PacketConn {
		t.Helper()
		c, err := net.ListenPacket(network, address)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	sa, sb := socket("udp4", "127.0.0.1:0"), socket(rightNetwork, rightBind)
	ea, eb := NewEndpoint(sa), NewEndpoint(sb)
	defer ea.Close()
	defer eb.Close()
	a := RouteCredentials{Channel: ChannelID{11}, ReceiveKey: Key{11}, SendKey: Key{12}, Generation: 1}
	b := RouteCredentials{Channel: ChannelID{12}, ReceiveKey: Key{13}, SendKey: Key{14}, Generation: 1}
	if err := ea.AddPairTo(eb, a, b); err != nil {
		t.Fatal(err)
	}
	peerA := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	peerB := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	left, err := DialUDP("udp4", sa.LocalAddr().String(), peerB, a)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	right, err := DialUDP(rightNetwork, sb.LocalAddr().String(), peerA, b)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()
	if _, err := left.WriteTo([]byte("different endpoints"), peerB); err != nil {
		t.Fatal(err)
	}
	right.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 128)
	n, _, err := right.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "different endpoints" {
		t.Fatalf("cross-endpoint forwarding: %q %v", buf[:n], err)
	}
	ea.Remove(a.Channel)
	right.WriteTo([]byte("revoked"), peerA)
	left.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := left.ReadFrom(buf); err == nil {
		t.Fatal("revoked pair still forwarded data")
	}
}

func TestIdleRelayHeartbeatAndFailure(t *testing.T) {
	socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewEndpoint(socket)
	defer endpoint.Close()
	a := RouteCredentials{Channel: ChannelID{31}, ReceiveKey: Key{31}, SendKey: Key{32}, Generation: 1}
	b := RouteCredentials{Channel: ChannelID{32}, ReceiveKey: Key{33}, SendKey: Key{34}, Generation: 1}
	if err := endpoint.AddPair(a, b); err != nil {
		t.Fatal(err)
	}
	client, err := DialUDP("udp4", socket.LocalAddr().String(), &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}, a)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _, _ = client.ReadFrom(make([]byte, 1500))
	}()
	select {
	case <-client.Failed():
		t.Fatal("idle healthy route incorrectly failed")
	case <-time.After(4 * time.Second):
	}
	endpoint.Close()
	select {
	case <-client.Failed():
	case <-time.After(5 * time.Second):
		t.Fatal("missing relay failure notification")
	}
	client.Close()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("blocked reader leaked")
	}
}
