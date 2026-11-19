package portowner

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func lookup(proto string, port int) string {
	return lookupIn("/proc", proto, port)
}

// lookupIn reads <proc>/net/{proto,proto6} for sockets on port (TCP only in
// LISTEN), then finds a process with one of their inodes among its file
// descriptors. Other accounts' processes are only visible with
// CAP_SYS_PTRACE (or as root).
func lookupIn(proc, proto string, port int) string {
	inodes := map[string]bool{}
	for _, f := range []string{proto, proto + "6"} {
		socketInodes(filepath.Join(proc, "net", f), proto == "tcp", port, inodes)
	}
	if len(inodes) == 0 {
		return ""
	}
	pids, _ := os.ReadDir(proc)
	for _, d := range pids {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(proc, d.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(proc, d.Name(), "fd", fd.Name()))
			if err != nil {
				continue
			}
			if in, ok := strings.CutPrefix(link, "socket:["); ok && inodes[strings.TrimSuffix(in, "]")] {
				name, _ := os.ReadFile(filepath.Join(proc, d.Name(), "comm"))
				return fmt.Sprintf("%s (pid %d)", strings.TrimSpace(string(name)), pid)
			}
		}
	}
	return ""
}

// socketInodes adds the inodes of sockets bound to port in a /proc/net table.
func socketInodes(path string, listenOnly bool, port int, out map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		// sl local_address rem_address st tx_queue:rx_queue tr:tm->when retrnsmt uid timeout inode
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		_, hexPort, ok := strings.Cut(fields[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(hexPort, 16, 16)
		if err != nil || int(p) != port {
			continue
		}
		if listenOnly && fields[3] != "0A" {
			continue
		}
		if fields[9] != "0" {
			out[fields[9]] = true
		}
	}
}
