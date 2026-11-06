package dhcp

import (
	"context"
	"net"
	"os"
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

// TestICMPPinger needs raw sockets (root); it is skipped otherwise.
func TestICMPPinger(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root for raw ICMP")
	}
	ctx := context.Background()
	alive, err := icmpPinger{}.Ping(ctx, net.IPv4(127, 0, 0, 1), time.Second)
	if err != nil {
		t.Skipf("raw ICMP unavailable here: %v", err)
	}
	if !alive {
		t.Error("127.0.0.1 didn't answer")
	}
	// TEST-NET-3: nothing answers.
	if alive, err := (icmpPinger{}).Ping(ctx, net.IPv4(203, 0, 113, 250), 300*time.Millisecond); err != nil || alive {
		t.Errorf("203.0.113.250 = %v, %v; want no answer", alive, err)
	}
}
