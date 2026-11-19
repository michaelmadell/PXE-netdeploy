package datalock

import "golang.org/x/sys/windows"

// makePrivate gives dir a protected access list, inherited by everything
// created in it: full control for SYSTEM, Administrators and the account
// running this process. The secrets key is protected with machine-scope
// DPAPI, so who can read the data directory is who can read the secrets.
func makePrivate(dir string) error {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	if sid := u.User.Sid.String(); sid != "S-1-5-18" {
		sddl += "(A;OICI;FA;;;" + sid + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
