//go:build !linux && !windows

package hostcheck

import "os"

func probe() Privileges { return Privileges{LowPorts: os.Geteuid() == 0} }

func missing(p Privileges) []string {
	if !p.LowPorts {
		return []string{"root"}
	}
	return nil
}
