package winsys

import (
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The fMask flags and nShow value RunAs passes to ShellExecuteExW.
const (
	// seeMaskNoCloseProcess asks for the started process's handle.
	seeMaskNoCloseProcess = 0x40
	// seeMaskNoAsync makes the call finish its work before it returns.
	seeMaskNoAsync = 0x100
	// seeMaskFlagNoUI keeps Windows from showing its own error dialogs.
	seeMaskFlagNoUI = 0x400
	// swHide starts the program without a window.
	swHide = 0
)

// procShellExecuteExW is ShellExecuteExW in shell32.dll, which
// golang.org/x/sys/windows does not wrap.
var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// shellExecuteInfo is SHELLEXECUTEINFOW. With natural alignment it is 112
// bytes on amd64 and arm64, as the Windows SDK lays it out; hIcon stands for
// the union of hIcon and hMonitor, both handles.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.HWND
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

// RunAs starts file with args as administrator, through the "runas" verb
// that shows the UAC prompt when this process is not elevated, and returns
// the started process's handle, which the caller closes. Each argument
// reaches the program as it is given. A declined prompt is
// windows.ERROR_CANCELLED.
func RunAs(file string, args []string) (windows.Handle, error) {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	path, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return 0, err
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	params, err := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if err != nil {
		return 0, err
	}
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		lpVerb:       verb,
		lpFile:       path,
		lpParameters: params,
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	if ok, _, err := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info))); ok == 0 {
		return 0, err
	}
	if info.hProcess == 0 {
		return 0, windows.ERROR_INVALID_HANDLE
	}
	return info.hProcess, nil
}
