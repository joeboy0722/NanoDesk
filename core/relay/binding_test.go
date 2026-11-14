package relay

import (
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func TestSharedSocketSTUNPermissionAndMapping(t *testing.T) {
	server, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := NewEndpoint(server)
	defer endpoint.Close()
	client, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request, err := stun.Build(stun.TransactionID, stun.BindingRequest)
	if err != nil {
		t.Fatal(err)
	}
	client.WriteTo(request.Raw, server.LocalAddr())
	client.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buffer := make([]byte, 512)
	if _, _, err := client.ReadFrom(buffer); err == nil {
		t.Fatal("unapproved source received a STUN response")
	}
	endpoint.SetBindingPermission(func(address net.Addr) bool { return address.String() == client.LocalAddr().String() })
	client.WriteTo(request.Raw, server.LocalAddr())
	client.SetReadDeadline(time.Now().Add(time.Second))
	n, _, err := client.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	response := &stun.Message{Raw: buffer[:n]}
	if err := response.Decode(); err != nil {
		t.Fatal(err)
	}
	var mapped stun.XORMappedAddress
	if err := mapped.GetFrom(response); err != nil {
		t.Fatal(err)
	}
	expected := client.LocalAddr().(*net.UDPAddr)
	if !mapped.IP.Equal(expected.IP) || mapped.Port != expected.Port || response.TransactionID != request.TransactionID {
		t.Fatal("incorrect mapped address or transaction")
	}
}
