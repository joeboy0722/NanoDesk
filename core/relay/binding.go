package relay

import (
	"net"
	"time"

	"github.com/pion/stun/v3"
)

type bindingPermission struct{ allow func(net.Addr) bool }

func (e *Endpoint) SetBindingPermission(allow func(net.Addr) bool) {
	if allow == nil {
		e.bindingPermission.Store(nil)
		return
	}
	e.bindingPermission.Store(&bindingPermission{allow})
}

// Binding requests share the configured UDP endpoint. They are accepted only
// from addresses belonging to live signaling sessions and have a bounded rate.
func (e *Endpoint) handleBinding(data []byte, address net.Addr) bool {
	if !stun.IsMessage(data) {
		return false
	}
	permission := e.bindingPermission.Load()
	if permission == nil || !permission.allow(address) || !e.bindingRate.AllowN(time.Now(), 1) {
		return true
	}
	message := &stun.Message{Raw: data}
	if message.Decode() != nil || message.Type != stun.BindingRequest {
		return true
	}
	udp, ok := address.(*net.UDPAddr)
	if !ok {
		return true
	}
	response, err := stun.Build(stun.NewTransactionIDSetter(message.TransactionID), stun.BindingSuccess,
		&stun.XORMappedAddress{IP: udp.IP, Port: udp.Port}, stun.Fingerprint)
	if err != nil {
		return true
	}
	e.conn.SetWriteDeadline(time.Now().Add(time.Second))
	n, err := e.conn.WriteTo(response.Raw, address)
	if n > 0 {
		e.traffic.sent.Add(uint64(n))
		e.traffic.record(n)
	}
	if err != nil {
		e.traffic.dropped.Add(1)
	}
	return true
}

func (p *Pool) SetBindingPermission(allow func(net.Addr) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bindingPermission = allow
	for _, socket := range p.sockets {
		socket.endpoint.SetBindingPermission(allow)
	}
}
