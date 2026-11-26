//go:build windows

package service

import (
	"os/user"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wtsapi32                  = windows.NewLazySystemDLL("wtsapi32.dll")
	wtsQuerySessionInfomation = wtsapi32.NewProc("WTSQuerySessionInformationW")
)

// WTS_INFO_CLASS values for WTSQuerySessionInformationW.
const (
	wtsUserName   = 5
	wtsDomainName = 7
)

// signedInToDesktop reports whether the user running this program has a
// desktop session on this machine: signed in at the screen or over Remote
// Desktop, locked or disconnected included. The service's task runs only in
// that session, so without one Task Scheduler takes /Run and runs nothing.
// An SSH session is not one: it runs in session 0, which is skipped.
func signedInToDesktop() (bool, error) {
	u, err := user.Current()
	if err != nil {
		return false, err
	}
	var sessions *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &sessions, &n); err != nil {
		return false, err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))
	for _, s := range unsafe.Slice(sessions, n) {
		if s.SessionID == 0 || (s.State != windows.WTSActive && s.State != windows.WTSConnected && s.State != windows.WTSDisconnected) {
			continue
		}
		name, err := sessionString(s.SessionID, wtsUserName)
		if err != nil || name == "" {
			continue
		}
		domain, _ := sessionString(s.SessionID, wtsDomainName)
		if sameUser(u.Username, domain, name) {
			return true, nil
		}
	}
	return false, nil
}

// sameUser reports whether DOMAIN\name, as user.Current gives it, is the
// user signed in to a session, whose domain and name Windows gives apart.
// Both ignore case, as Windows does.
func sameUser(current, domain, name string) bool {
	if d, n, ok := strings.Cut(current, `\`); ok {
		return strings.EqualFold(n, name) && (domain == "" || strings.EqualFold(d, domain))
	}
	return strings.EqualFold(current, name)
}

// sessionString is one of a session's strings, such as its user's name.
func sessionString(id uint32, class uint32) (string, error) {
	var buf *uint16
	var size uint32
	r, _, err := wtsQuerySessionInfomation.Call(0, uintptr(id), uintptr(class), uintptr(unsafe.Pointer(&buf)), uintptr(unsafe.Pointer(&size)))
	if r == 0 {
		return "", err
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(buf)))
	return windows.UTF16PtrToString(buf), nil
}
