//go:build !windows

package datalock

// makePrivate: MkdirAll's 0700 (under the server's umask 077) already is.
func makePrivate(string) error { return nil }
