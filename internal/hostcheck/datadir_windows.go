package hostcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PrivateFiles are checked as well as the directory: a file can carry its
// own access list. The secrets key gets a protected one when written (ADR 0021).
var PrivateFiles = []string{"secrets.key", "netdeploy.db"}

// broad are the groups that make a file readable by any local account.
var broad = map[string]string{
	"S-1-1-0":      "Everyone",
	"S-1-5-11":     "Authenticated Users",
	"S-1-5-32-545": "Users",
	"S-1-5-4":      "Interactive",
}

// readRights: any of these lets the holder read file contents.
const readRights = windows.FILE_READ_DATA | windows.GENERIC_READ | windows.GENERIC_ALL | windows.FILE_GENERIC_READ

// DataDir lists what makes the data directory readable by other accounts
// (ADR 0031): an access list entry allowing Everyone, Authenticated Users,
// Users or Interactive to read. The secrets key is protected with
// machine-scope DPAPI, so reading the file is enough to unwrap it.
func DataDir(dir string) []string {
	var out []string
	check := func(path, what string) {
		if _, err := os.Stat(path); err != nil {
			return
		}
		groups, err := readableBy(path)
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", what, err))
			return
		}
		if len(groups) > 0 {
			out = append(out, fmt.Sprintf("%s can be read by %s", what, strings.Join(groups, ", ")))
		}
	}
	check(dir, "the data directory "+dir)
	for _, f := range PrivateFiles {
		check(filepath.Join(dir, f), f)
	}
	if len(out) > 0 {
		out = append(out, fmt.Sprintf(`fix with: icacls "%s" /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F" /T`, dir)+
			" (SYSTEM and Administrators; add the account that runs netdeployd if it is neither)")
	}
	return out
}

// readableBy names the broad groups the path's access list lets read it.
func readableBy(path string) ([]string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	if acl == nil {
		return []string{"everyone (no access list)"}, nil
	}
	seen := map[string]bool{}
	var out []string
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Mask&readRights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if name, ok := broad[sid]; ok && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out, nil
}
