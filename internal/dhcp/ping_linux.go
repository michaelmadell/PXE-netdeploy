package dhcp

import (
	"net"
	"os"
	"syscall"
)

// listenDatagramICMP opens an unprivileged ICMP socket (SOCK_DGRAM,
// IPPROTO_ICMP), allowed for groups in net.ipv4.ping_group_range.
func listenDatagramICMP() (net.PacketConn, error) {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_CLOEXEC, syscall.IPPROTO_ICMP)
	if err != nil {
		return nil, err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{}); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "icmp")
	defer f.Close() // FilePacketConn dups the descriptor
	return net.FilePacketConn(f)
}
