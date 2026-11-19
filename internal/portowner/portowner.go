// Package portowner finds the process holding a local TCP or UDP port, so a
// bind failure can say who is in the way (BindFailure.ownerProcess, ADR
// 0030). Best effort: "" when the OS won't say.
package portowner

// Lookup returns "name (pid N)" for the process bound to port (proto "tcp"
// listening, or "udp"), or "" if none is found.
func Lookup(proto string, port int) string {
	if port <= 0 || port > 65535 {
		return ""
	}
	return lookup(proto, port)
}
