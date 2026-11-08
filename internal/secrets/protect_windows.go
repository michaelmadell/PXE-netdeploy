package secrets

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// protect wraps the key with DPAPI in machine scope: any account on this
// machine that can read the file can unwrap it, so restrict() limits the
// file to SYSTEM, Administrators and the creating account.
func protect(key []byte) (byte, []byte, error) {
	in := blob(key)
	ent := blob([]byte(dpapiEntropyID))
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &ent, 0, nil,
		windows.CRYPTPROTECT_LOCAL_MACHINE|windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return 0, nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return protectDPAPI, append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func unprotect(payload []byte) ([]byte, error) {
	in := blob(payload)
	ent := blob([]byte(dpapiEntropyID))
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &ent, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}

func blob(b []byte) windows.DataBlob {
	if len(b) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(b)), Data: &b[0]}
}

// restrict gives the file a protected DACL: full control for SYSTEM,
// Administrators and the current account only.
func restrict(path string) error {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + u.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
