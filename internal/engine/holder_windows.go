package engine

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// netstatTool asks netstat, which every Windows has, and names the process
// itself: netstat gives only the pid. Without -p, which would limit it to
// IPv4, it lists IPv6 listeners too.
var netstatTool = holderTool{
	name: "netstat",
	args: func(int) []string { return []string{"-ano"} },
	parse: func(out string, port int) string {
		pid := parseNetstat(out, port)
		if pid == 0 {
			return ""
		}
		return describeHolder(imageName(pid), pid)
	},
}

// holderTools are the tools PortHolder asks, in order.
func holderTools() []holderTool { return []holderTool{netstatTool} }

// imageName is the name of the program pid runs, without its directory or
// .exe, or "" when it cannot be read, as for some system processes.
func imageName(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	name := filepath.Base(windows.UTF16ToString(buf[:size]))
	if ext := filepath.Ext(name); strings.EqualFold(ext, ".exe") {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}
