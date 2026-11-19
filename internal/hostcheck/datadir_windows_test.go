package hostcheck

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelmadell/pxe-netdeploy/internal/datalock"
	"golang.org/x/sys/windows"
)

func setDACL(t *testing.T, path, sddl string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
}

// A data directory datalock creates is private; one that Users can read is reported.
func TestDataDirACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	l, err := datalock.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if got := DataDir(dir); len(got) != 0 {
		t.Fatalf("new data directory not private: %v", got)
	}
	u, _ := windows.GetCurrentProcessToken().GetTokenUser()
	setDACL(t, dir, "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;"+u.User.Sid.String()+")(A;OICI;0x1200a9;;;BU)")
	got := strings.Join(DataDir(dir), "\n")
	if !strings.Contains(got, "the data directory") || !strings.Contains(got, "Users") || !strings.Contains(got, "icacls") {
		t.Fatalf("Users' read access not reported: %s", got)
	}
}
