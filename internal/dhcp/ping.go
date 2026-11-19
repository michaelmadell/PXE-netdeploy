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

// icmpPinger sends one ICMP echo: over a raw socket when the process may
// open one (root or CAP_NET_RAW on Linux, elevated on Windows), else over
// Linux's unprivileged ICMP datagram socket when net.ipv4.ping_group_range
// allows it (ADR 0030).
type icmpPinger struct{}

const (
	icmpEcho      = 8
	icmpEchoReply = 0
)

// ICMP modes (Capabilities.privileges.icmp).
const (
	ICMPRaw      = "raw"
	ICMPDatagram = "datagram"
	ICMPNone     = "none"
)

// listenICMP opens the best ICMP socket this process may: raw, then
// datagram. err is the raw socket's error when neither opens.
func listenICMP() (net.PacketConn, string, error) {
	c, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err == nil {
		return c, ICMPRaw, nil
	}
	if d, derr := listenDatagramICMP(); derr == nil {
		return d, ICMPDatagram, nil
	}
	return nil, ICMPNone, err
}

// ProbeICMP reports which ICMP socket the ping check can use.
func ProbeICMP() string {
	c, mode, err := listenICMP()
	if err != nil {
		return ICMPNone
	}
	c.Close()
	return mode
}

func (icmpPinger) Ping(ctx context.Context, ip net.IP, timeout time.Duration) (bool, error) {
	c, mode, err := listenICMP()
	if err != nil {
		return false, err
	}
	defer c.Close()

	var idb [4]byte
	if _, err := rand.Read(idb[:]); err != nil {
		return false, err
	}
	id, seq := binary.BigEndian.Uint16(idb[:2]), binary.BigEndian.Uint16(idb[2:])
	var to net.Addr = &net.IPAddr{IP: ip}
	if mode == ICMPDatagram {
		to = &net.UDPAddr{IP: ip}
	}
	if _, err := c.WriteTo(echoRequest(id, seq), to); err != nil {
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
		// The net package strips the IPv4 header from raw ICMP reads; a
		// datagram socket never had one.
		n, peer, err := c.ReadFrom(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return false, nil
			}
			return false, err
		}
		var from net.IP
		switch pa := peer.(type) {
		case *net.IPAddr:
			from = pa.IP
		case *net.UDPAddr:
			from = pa.IP
		}
		if !from.Equal(ip) || n < 8 || buf[0] != icmpEchoReply || binary.BigEndian.Uint16(buf[6:]) != seq {
			continue
		}
		// A datagram socket's kernel replaces the identifier with its own.
		if mode == ICMPRaw && binary.BigEndian.Uint16(buf[4:]) != id {
			continue
		}
		return true, nil
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
