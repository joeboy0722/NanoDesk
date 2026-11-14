package relay

import (
	"fmt"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type benchmarkPair struct {
	sync.Mutex
	left, right *ClientConn
	remote      *net.UDPAddr
}

// This loopback benchmark measures the full client+relay process. It is useful
// for repeatable port-pool comparisons, not as a physical NIC capacity claim.
func BenchmarkRelayPorts(b *testing.B) {
	for _, ports := range []int{1, 2, 5} {
		for _, payloadSize := range []int{64, 1200} {
			for _, paced := range []bool{true, false} {
				mode := "saturated"
				if paced {
					mode = "paced"
				}
				b.Run(fmt.Sprintf("ports=%d/payload=%d/%s", ports, payloadSize, mode), func(b *testing.B) {
					benchmarkRelayPorts(b, ports, payloadSize, paced)
				})
			}
		}
	}
}

func benchmarkRelayPorts(b *testing.B, ports, payloadSize int, paced bool) {
	var endpoints []*Endpoint
	for range ports {
		socket, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			b.Fatal(err)
		}
		endpoint := NewEndpoint(socket)
		endpoints = append(endpoints, endpoint)
		b.Cleanup(func() { endpoint.Close() })
	}
	var pairs []*benchmarkPair
	remoteA := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 1), Port: 10001}
	remoteB := &net.UDPAddr{IP: net.IPv4(198, 18, 0, 2), Port: 10002}
	for i := 0; i < 20; i++ {
		endpoint := endpoints[i%len(endpoints)]
		a, err := newCredentials(1)
		if err != nil {
			b.Fatal(err)
		}
		c, err := newCredentials(1)
		if err != nil {
			b.Fatal(err)
		}
		if err := endpoint.AddPair(a, c); err != nil {
			b.Fatal(err)
		}
		left, err := DialUDP("udp4", endpoint.conn.LocalAddr().String(), remoteB, a)
		if err != nil {
			b.Fatal(err)
		}
		right, err := DialUDP("udp4", endpoint.conn.LocalAddr().String(), remoteA, c)
		if err != nil {
			left.Close()
			b.Fatal(err)
		}
		b.Cleanup(func() { left.Close(); right.Close() })
		pairs = append(pairs, &benchmarkPair{left: left, right: right, remote: remoteB})
		go func() {
			buffer := make([]byte, MaxPayload)
			for {
				n, address, err := right.ReadFrom(buffer)
				if err != nil {
					return
				}
				if _, err := right.WriteTo(buffer[:n], address); err != nil {
					return
				}
			}
		}()
	}
	var next, errors, latency atomic.Uint64
	var startDrops uint64
	for _, endpoint := range endpoints {
		startDrops += endpoint.Stats().DroppedPackets
	}
	b.SetBytes(int64(payloadSize * 2))
	b.ReportAllocs()
	cpuStart := benchmarkProcessCPU(b)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		payload := make([]byte, payloadSize)
		buffer := make([]byte, MaxPayload)
		for pb.Next() {
			pair := pairs[(next.Add(1)-1)%uint64(len(pairs))]
			pair.Lock()
			if paced {
				time.Sleep(100 * time.Millisecond)
			}
			start := time.Now()
			pair.left.SetDeadline(start.Add(time.Second))
			_, err := pair.left.WriteTo(payload, pair.remote)
			if err == nil {
				n, _, readErr := pair.left.ReadFrom(buffer)
				err = readErr
				if err == nil && n != len(payload) {
					err = fmt.Errorf("incorrect datagram size")
				}
			}
			latency.Add(uint64(time.Since(start)))
			if err != nil {
				errors.Add(1)
			}
			pair.Unlock()
		}
	})
	b.StopTimer()
	cpuUsed := benchmarkProcessCPU(b) - cpuStart
	b.ReportMetric(float64(cpuUsed)/float64(b.Elapsed())/float64(runtime.NumCPU())*100, "process-cpu-percent")
	var drops uint64
	for _, endpoint := range endpoints {
		drops += endpoint.Stats().DroppedPackets
	}
	b.ReportMetric(float64(latency.Load())/float64(b.N)/1e6, "mean-rtt-ms")
	b.ReportMetric(float64(errors.Load()), "errors")
	b.ReportMetric(float64(drops-startDrops), "queue-drops")
}

func benchmarkProcessCPU(b *testing.B) time.Duration {
	b.Helper()
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		b.Fatal(err)
	}
	var created, exited, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		b.Fatal(err)
	}
	ticks := func(t syscall.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }
	return time.Duration((ticks(kernel) + ticks(user)) * 100)
}
