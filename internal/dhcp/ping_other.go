//go:build !linux

package dhcp

import (
	"errors"
	"net"
)

// listenDatagramICMP: unprivileged ICMP sockets are a Linux feature here.
func listenDatagramICMP() (net.PacketConn, error) {
	return nil, errors.New("no unprivileged ICMP socket on this OS")
}
