package hostcheck

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// Capability bits (linux/capability.h).
const (
	bitNetBindService = 10
	bitNetRaw         = 13
)

func probe() Privileges {
	if os.Geteuid() == 0 {
		return Privileges{LowPorts: true}
	}
	eff := effectiveCaps("/proc/self/status")
	low := eff&(1<<bitNetBindService) != 0 || unprivilegedPortStart("/proc/sys/net/ipv4/ip_unprivileged_port_start") <= 67
	return Privileges{LowPorts: low}
}

func missing(p Privileges) []string {
	var m []string
	if !p.LowPorts {
		m = append(m, CapNetBindService)
	}
	if p.ICMP != "raw" {
		m = append(m, CapNetRaw)
	}
	return m
}

// effectiveCaps reads CapEff from a /proc/<pid>/status file; 0 if unreadable.
func effectiveCaps(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "CapEff:"); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// unprivilegedPortStart reads the sysctl; 1024 (the default) if unreadable.
func unprivilegedPortStart(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 1024
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 1024
	}
	return n
}
