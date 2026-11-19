//go:build !windows

package hostcheck

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// PrivateFiles are the data directory's files no other account may read:
// keys, the setup code, and the database (password, session and token
// hashes, sealed secrets) with SQLite's WAL files.
var PrivateFiles = []string{"secrets.key", "admin-key.pem", "content-ca-key.enc", "setup-code.txt",
	"netdeploy.db", "netdeploy.db-wal", "netdeploy.db-shm"}

// DataDir lists what makes the data directory less than private: group or
// other access, or an owner other than this process's account.
func DataDir(dir string) []string {
	var out []string
	check := func(path, what string, isDir bool) {
		fi, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", what, err))
			return
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			fix := "chmod 600"
			if isDir {
				fix = "chmod 700"
			}
			out = append(out, fmt.Sprintf("%s can be read by other accounts (mode %04o; %s %s)", what, perm, fix, path))
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
			out = append(out, fmt.Sprintf("%s is owned by uid %d, not the server's account (uid %d)", what, st.Uid, os.Geteuid()))
		}
	}
	check(dir, "the data directory "+dir, true)
	for _, f := range PrivateFiles {
		check(filepath.Join(dir, f), f, false)
	}
	return out
}
