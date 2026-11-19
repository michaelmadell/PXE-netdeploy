package hostcheck

import "golang.org/x/sys/windows"

// Windows doesn't restrict low ports; raw ICMP and DISM need elevation.
func probe() Privileges {
	return Privileges{LowPorts: true, Elevated: windows.GetCurrentProcessToken().IsElevated()}
}

func missing(p Privileges) []string {
	if !p.Elevated {
		return []string{Elevation}
	}
	return nil
}
