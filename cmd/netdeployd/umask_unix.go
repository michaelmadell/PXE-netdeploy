//go:build !windows

package main

import "syscall"

// privateFiles makes everything the server and its offline commands create
// readable by its own account only (ADR 0030): the database, certificates
// and keys, the content store, backups and exports. The data directory
// check warns about any of them that other accounts can read.
func privateFiles() { syscall.Umask(0o077) }
