package dhcp

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestChecksum(t *testing.T) {
	// RFC 1071's example words sum to ddf2; the checksum is its complement.
	if got := checksum([]byte{0x00, 0x01, 0xf2, 0x03, 0xf4, 0xf5, 0xf6, 0xf7}); got != ^uint16(0xddf2) {
		t.Fatalf("checksum = %04x", got)
	}
	if b := echoRequest(1, 2); checksum(b) != 0 {
		t.Fatal("echo request doesn't checksum to zero")
	}
}

// TestICMPPinger needs a raw socket (root or CAP_NET_RAW on Linux, an
// elevated shell on Windows) or, on Linux, the datagram ICMP socket
// (net.ipv4.ping_group_range, open on most distributions and CI); it is
// skipped when neither opens.
func TestICMPPinger(t *testing.T) {
	ctx := context.Background()
	mode := ProbeICMP()
	t.Logf("ICMP socket: %s", mode)
	alive, err := icmpPinger{}.Ping(ctx, net.IPv4(127, 0, 0, 1), time.Second)
	if err != nil {
		if mode != ICMPNone {
			t.Fatalf("%s socket opened for the probe but the ping failed: %v", mode, err)
		}
		t.Skipf("no ICMP socket here: %v", err)
	}
	if !alive {
		t.Error("127.0.0.1 didn't answer")
	}
	// TEST-NET-3: nothing answers.
	if alive, err := (icmpPinger{}).Ping(ctx, net.IPv4(203, 0, 113, 250), 300*time.Millisecond); err != nil || alive {
		t.Errorf("203.0.113.250 = %v, %v; want no answer", alive, err)
	}
}
