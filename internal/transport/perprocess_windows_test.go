package transport

import "golang.org/x/sys/windows"

// stillActive is the exit code GetExitCodeProcess reports for a process
// that is running.
const stillActive = 259

// processGone reports whether the process with the given pid has exited.
func processGone(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code != stillActive
}
