package dhcp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

// Pinger probes whether an address is in use before it is offered.
type Pinger interface {
	// Ping reports whether ip answered an echo request within timeout. An
	// error means the check couldn't run (e.g. no privilege for ICMP).
	Ping(ctx context.Context, ip net.IP, timeout time.Duration) (bool, error)
}

// icmpPinger sends one ICMP echo over a raw socket. That needs root on Linux
// and administrator on Windows (to verify on Windows), as port 67 does.
type icmpPinger struct{}

const (
	icmpEcho      = 8
	icmpEchoReply = 0
)

func (icmpPinger) Ping(ctx context.Context, ip net.IP, timeout time.Duration) (bool, error) {
	c, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		return false, err
	}
	defer c.Close()

	var idb [4]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return false, err
	}
	id, seq := binary.BigEndian.Uint16(idb[:2]), binary.BigEndian.Uint16(idb[2:])
	if _, err := c.WriteTo(echoRequest(id, seq), &net.IPAddr{IP: ip}); err != nil {
		return false, err
	}

	deadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := c.SetReadDeadline(deadline); err != nil {
		return false, err
	}
	buf := make([]byte, 1500)
	for {
		// The net package strips the IPv4 header from raw ICMP reads.
		n, peer, err := c.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return false, nil
			}
			return false, err
		}
		pa, ok := peer.(*net.IPAddr)
		if !ok || !pa.IP.Equal(ip) || n < 8 {
			continue
		}
		if buf[0] == icmpEchoReply && binary.BigEndian.Uint16(buf[4:]) == id && binary.BigEndian.Uint16(buf[6:]) == seq {
			return true, nil
		}
	}
}

// echoRequest builds an ICMP echo request (RFC 792).
func echoRequest(id, seq uint16) []byte {
	b := make([]byte, 8, 8+19)
	b[0] = icmpEcho
	binary.BigEndian.PutUint16(b[4:], id)
	binary.BigEndian.PutUint16(b[6:], seq)
	b = append(b, "netdeploy pingcheck"...)
	binary.BigEndian.PutUint16(b[2:], checksum(b))
	return b
}

// checksum is the Internet checksum (RFC 1071).
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
