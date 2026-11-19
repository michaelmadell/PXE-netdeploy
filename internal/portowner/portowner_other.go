//go:build !linux && !windows

package portowner

func lookup(string, int) string { return "" }
