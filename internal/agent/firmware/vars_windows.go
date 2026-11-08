//go:build windows

package firmware

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// globalGUID is EFI_GLOBAL_VARIABLE.
const globalGUID = "{8BE4DF61-93CA-11D2-AA0D-00E098032B8C}"

// NV | BS | RT, as BootOrder and BootNext have.
const attrs = 0x7

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	getVar   = kernel32.NewProc("GetFirmwareEnvironmentVariableExW")
	setVar   = kernel32.NewProc("SetFirmwareEnvironmentVariableExW")
)

// Windows reads and writes EFI variables through kernel32. Open enables
// SeSystemEnvironmentPrivilege first (WinPE runs as SYSTEM, which holds it).
type Windows struct{}

// Open returns the system's variables.
func Open() (Windows, error) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return Windows{}, fmt.Errorf("open process token: %w", err)
	}
	defer tok.Close()
	var luid windows.LUID
	name, _ := windows.UTF16PtrFromString("SeSystemEnvironmentPrivilege")
	if err := windows.LookupPrivilegeValue(nil, name, &luid); err != nil {
		return Windows{}, fmt.Errorf("look up SeSystemEnvironmentPrivilege: %w", err)
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	if err := windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil); err != nil {
		return Windows{}, fmt.Errorf("enable SeSystemEnvironmentPrivilege: %w", err)
	}
	return Windows{}, nil
}

func (Windows) Get(name string) ([]byte, error) {
	n, _ := windows.UTF16PtrFromString(name)
	g, _ := windows.UTF16PtrFromString(globalGUID)
	buf := make([]byte, 4096)
	var attr uint32
	r, _, err := getVar.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(g)), uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)), uintptr(unsafe.Pointer(&attr)))
	if r == 0 {
		if errors.Is(err, syscall.Errno(windows.ERROR_ENVVAR_NOT_FOUND)) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return buf[:r], nil
}

func (Windows) Set(name string, value []byte) error {
	n, _ := windows.UTF16PtrFromString(name)
	g, _ := windows.UTF16PtrFromString(globalGUID)
	var p uintptr
	if len(value) > 0 {
		p = uintptr(unsafe.Pointer(&value[0]))
	}
	r, _, err := setVar.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(g)), p, uintptr(len(value)), attrs)
	if r == 0 {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
