package cli

import "strings"

// defaultSystemRoot is where Windows lives when %SystemRoot% is not set.
const defaultSystemRoot = `C:\Windows`

// checkSSHVendor reports whether sshPath, the ssh Nearhost runs on Windows,
// is Microsoft's OpenSSH: the one in %SystemRoot%\System32\OpenSSH, or the
// MSI build in %ProgramFiles%\OpenSSH. Another ssh (Git for Windows' MSYS
// one is the usual) reads another config file and agent, and forwards fail
// in confusing ways, so it is a warning. A system with a different root
// gets advice naming its own directory.
//
// It compares strings and never calls filepath, so it reads Windows paths
// the same on any host. It does not expand 8.3 short names or symlinks, and
// it expects a 64-bit process, which Nearhost is: a 32-bit one would see
// System32 redirected to SysWOW64.
func checkSSHVendor(sshPath, systemRoot, programFiles string) check {
	c := check{name: "ssh vendor", detail: sshPath}
	if systemRoot == "" {
		systemRoot = defaultSystemRoot
	}
	system := strings.TrimRight(strings.ReplaceAll(systemRoot, "/", `\`), `\`) + `\System32\OpenSSH`
	dirs := []string{system}
	if programFiles != "" {
		dirs = append(dirs, programFiles+`\OpenSSH`)
	}
	for _, dir := range dirs {
		if underWindowsDir(sshPath, dir) {
			return c
		}
	}
	c.level = levelWarn
	c.detail = "ssh is " + sshPath + ", not Microsoft's OpenSSH."
	c.fix = "Nearhost is tested with Microsoft's; put " + system + " first on PATH."
	return c
}

// underWindowsDir reports whether path names something inside dir, both
// Windows paths. It compares whole segments without regard to case, and
// treats / as \, so dir\OpenSSHx does not match dir\OpenSSH.
func underWindowsDir(path, dir string) bool {
	p, d := windowsSegments(path), windowsSegments(dir)
	if len(p) <= len(d) {
		return false
	}
	for i := range d {
		if p[i] != d[i] {
			return false
		}
	}
	return true
}

// windowsSegments splits a Windows path into lower-case segments, dropping
// empty and "." ones.
func windowsSegments(path string) []string {
	var out []string
	for _, s := range strings.Split(strings.ReplaceAll(path, "/", `\`), `\`) {
		if s != "" && s != "." {
			out = append(out, strings.ToLower(s))
		}
	}
	return out
}
