package portowner

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	iphlpapi                = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = iphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUDPTable = iphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet                 = 2
	tcpTableOwnerPIDListen = 3 // TCP_TABLE_OWNER_PID_LISTENER
	udpTableOwnerPID       = 1 // UDP_TABLE_OWNER_PID
)

// lookup reads the owner-PID tables (IPv4) and names the process.
func lookup(proto string, port int) string {
	proc, class, rowSize, portAt, pidAt := procGetExtendedUDPTable, uintptr(udpTableOwnerPID), 12, 4, 8
	if proto == "tcp" {
		// MIB_TCPROW_OWNER_PID: state, localAddr, localPort, remoteAddr, remotePort, pid.
		proc, class, rowSize, portAt, pidAt = procGetExtendedTCPTable, tcpTableOwnerPIDListen, 24, 8, 20
	}
	var size uint32
	proc.Call(0, uintptr(unsafe.Pointer(&size)), 0, afInet, class, 0)
	if size == 0 {
		return ""
	}
	buf := make([]byte, size)
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, class, 0); r != 0 {
		return ""
	}
	n := int(binary.LittleEndian.Uint32(buf))
	for i := 0; i < n; i++ {
		row := buf[4+i*rowSize:]
		if len(row) < rowSize {
			break
		}
		// The port is in network byte order in the low 16 bits.
		p := int(row[portAt])<<8 | int(row[portAt+1])
		if p != port {
			continue
		}
		pid := binary.LittleEndian.Uint32(row[pidAt:])
		return fmt.Sprintf("%s (pid %d)", processName(pid), pid)
	}
	return ""
}

func processName(pid uint32) string {
	switch pid {
	case 0:
		return "System Idle Process"
	case 4:
		return "System" // e.g. http.sys holding port 80
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "unknown process"
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "unknown process"
	}
	return filepath.Base(windows.UTF16ToString(buf[:n]))
}
