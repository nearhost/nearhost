package update

import (
	"path"
	"slices"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	// Go's default GOPATH is under USERPROFILE on Windows.
	home := map[string]string{"HOME": "/Users/me", "USERPROFILE": "/Users/me"}
	for _, tc := range []struct {
		name, exe string
		env       map[string]string
		kind      Kind
		command   string
	}{
		{"homebrew arm", "/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost", home, Homebrew, "brew upgrade --cask nearhost/tap/nearhost"},
		{"homebrew linux", "/home/linuxbrew/.linuxbrew/Caskroom/nearhost/0.5.0/nearhost", home, Homebrew, "brew upgrade --cask nearhost/tap/nearhost"},
		{"npm global", "/usr/local/lib/node_modules/@nearhost/cli/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", home, NPM, "npm install -g @nearhost/cli@latest"},
		{"npm hoisted", "/Users/me/.nvm/versions/node/v22.0.0/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost", home, NPM, "npm install -g @nearhost/cli@latest"},
		{"pnpm global", "/Users/me/Library/pnpm/global/5/node_modules/.pnpm/@nearhost+cli-darwin-arm64@0.5.0/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", home, PNPM, "pnpm add -g @nearhost/cli@latest"},
		{"bun global", "/Users/me/.bun/install/global/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", home, Bun, "bun add -g @nearhost/cli@latest"},
		{"npx", "/Users/me/.npm/_npx/1a2b3c/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", home, Ephemeral, ""},
		{"bunx", "/private/tmp/bunx-501-@nearhost/cli@latest/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", home, Ephemeral, ""},
		{"go default", "/Users/me/go/bin/nearhost", home, Go, "go install github.com/nearhost/nearhost/cmd/nearhost@latest"},
		{"go GOPATH", "/work/gopath/bin/nearhost", map[string]string{"HOME": "/Users/me", "GOPATH": "/work/gopath:/other"}, Go, "go install github.com/nearhost/nearhost/cmd/nearhost@latest"},
		{"go GOBIN", "/opt/gobin/nearhost", map[string]string{"HOME": "/Users/me", "GOBIN": "/opt/gobin"}, Go, "go install github.com/nearhost/nearhost/cmd/nearhost@latest"},
		{"script usr local", "/usr/local/bin/nearhost", home, Script, "curl -fsSL https://nearhost.dev/install.sh | sh"},
		{"script local bin", "/Users/me/.local/bin/nearhost", home, Script, "curl -fsSL https://nearhost.dev/install.sh | sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := DetectOn("linux", tc.exe, env(tc.env))
			if m.Kind != tc.kind {
				t.Fatalf("Kind = %v, want %v", m.Kind, tc.kind)
			}
			if got := m.String(); got != tc.command {
				t.Fatalf("command = %q, want %q", got, tc.command)
			}
		})
	}
}

func TestScriptInstallsOverTheRunningBinary(t *testing.T) {
	dir := path.Join("/opt", "tools", "bin")
	m := DetectOn("linux", path.Join(dir, "nearhost"), func(string) string { return "" })
	if !slices.Contains(m.Env, "NEARHOST_INSTALL_DIR="+dir) {
		t.Fatalf("Env = %v, want the binary's directory as NEARHOST_INSTALL_DIR", m.Env)
	}
	if m.Args[0] != "sh" || !strings.Contains(m.Args[2], "install.sh | sh") {
		t.Fatalf("Args = %v", m.Args)
	}
}

func TestGoBinHomeForEachOS(t *testing.T) {
	// Git Bash sets HOME on Windows, but Go's default GOPATH there is still
	// %USERPROFILE%\go.
	env := map[string]string{"HOME": "/h", "USERPROFILE": "/u"}
	getenv := func(name string) string { return env[name] }
	for _, tc := range []struct {
		goos, dir string
		want      bool
	}{
		{"windows", path.Join("/u", "go", "bin"), true},
		{"windows", path.Join("/h", "go", "bin"), false},
		{"darwin", path.Join("/h", "go", "bin"), true},
		{"linux", path.Join("/u", "go", "bin"), false},
	} {
		if got := goBin(tc.dir, getenv, tc.goos); got != tc.want {
			t.Errorf("goBin(%s) on %s = %v, want %v", tc.dir, tc.goos, got, tc.want)
		}
	}
}

func TestGoBinIgnoresCaseOnWindows(t *testing.T) {
	// No drive letters: the test runs on Unix too, where GOPATH lists split
	// on the colon.
	env := map[string]string{"USERPROFILE": `\Users\Ada`}
	getenv := func(name string) string { return env[name] }
	dir := `\users\ada\go\bin`
	if !goBin(dir, getenv, "windows") {
		t.Errorf("goBin(%s) on windows = false for USERPROFILE %s", dir, env["USERPROFILE"])
	}
	if goBin(dir, getenv, "linux") {
		t.Errorf("goBin(%s) on linux = true, though case matters there", dir)
	}
	env = map[string]string{"GOBIN": `\Go\Bin`}
	if !goBin(`\go\bin`, getenv, "windows") {
		t.Error("goBin on windows = false for GOBIN differing only in case")
	}
}

func TestDetectOnWindows(t *testing.T) {
	getenv := func(k string) string { return map[string]string{"USERPROFILE": `C:\Users\José Núñez`}[k] }
	for _, tc := range []struct {
		name, exe string
		kind      Kind
		command   string
	}{
		{"npm", `C:\Users\José Núñez\AppData\Roaming\npm\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`, NPM, "npm install -g @nearhost/cli@latest"},
		{"npm upper case", `C:\USERS\X\AppData\Roaming\npm\NODE_MODULES\@nearhost\cli-win32-x64\bin\nearhost.exe`, NPM, "npm install -g @nearhost/cli@latest"},
		{"pnpm", `C:\Users\x\AppData\Local\pnpm\global\5\node_modules\.pnpm\@nearhost+cli-win32-x64@0.13.0\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`, PNPM, "pnpm add -g @nearhost/cli@latest"},
		{"bun", `C:\Users\x\.bun\install\global\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`, Bun, "bun add -g @nearhost/cli@latest"},
		{"npx", `C:\Users\x\AppData\Local\npm-cache\_npx\1a2b\node_modules\@nearhost\cli-win32-x64\bin\nearhost.exe`, Ephemeral, ""},
		{"go", `C:\Users\José Núñez\go\bin\nearhost.exe`, Go, "go install github.com/nearhost/nearhost/cmd/nearhost@latest"},
		{"go other case", `c:\users\josé núñez\GO\bin\nearhost.exe`, Go, "go install github.com/nearhost/nearhost/cmd/nearhost@latest"},
		{"script", `C:\Tools\nearhost.exe`, Script, InstallScriptWindows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := DetectOn("windows", tc.exe, getenv)
			if m.Kind != tc.kind || m.String() != tc.command {
				t.Fatalf("got %v %q, want %v %q", m.Kind, m.String(), tc.kind, tc.command)
			}
		})
	}
	m := DetectOn("windows", `C:\Tools\nearhost.exe`, getenv)
	wantArgs := []string{"powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "irm https://nearhost.dev/install.ps1 | iex"}
	// NEARHOST_NO_PATH: an update never changes the user's PATH. An empty
	// NEARHOST_VERSION: a version pinned in the user's session is not
	// reinstalled.
	if !slices.Equal(m.Args, wantArgs) || !slices.Equal(m.Env, []string{`NEARHOST_INSTALL_DIR=C:\Tools`, "NEARHOST_NO_PATH=1", "NEARHOST_VERSION="}) || m.Dir != `C:\Tools` {
		t.Fatalf("Args %q Env %q Dir %q", m.Args, m.Env, m.Dir)
	}
	if InstallScriptWindows != "irm https://nearhost.dev/install.ps1 | iex" {
		t.Fatalf("InstallScriptWindows = %q", InstallScriptWindows)
	}
	spaced := DetectOn("windows", `C:\Users\José Núñez\bin\nearhost.exe`, getenv)
	if !slices.Equal(spaced.Env, []string{`NEARHOST_INSTALL_DIR=C:\Users\José Núñez\bin`, "NEARHOST_NO_PATH=1", "NEARHOST_VERSION="}) {
		t.Fatalf("Env = %q", spaced.Env)
	}
}

func TestPinned(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, tc := range []struct{ exe, want string }{
		{"/usr/local/lib/node_modules/@nearhost/cli-linux-x64/bin/nearhost", "npm install -g @nearhost/cli@0.13.0-rc.2"},
		{"/Users/me/Library/pnpm/global/5/node_modules/.pnpm/x/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", "pnpm add -g @nearhost/cli@0.13.0-rc.2"},
		{"/Users/me/.bun/install/global/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost", "bun add -g @nearhost/cli@0.13.0-rc.2"},
		{"/Users/me/go/bin/nearhost", "go install github.com/nearhost/nearhost/cmd/nearhost@v0.13.0-rc.2"},
		{"/usr/local/bin/nearhost", "curl -fsSL https://nearhost.dev/install.sh | NEARHOST_VERSION=v0.13.0-rc.2 sh"},
	} {
		base := DetectOn("linux", tc.exe, func(k string) string { return map[string]string{"HOME": "/Users/me"}[k] })
		before := slices.Clone(base.Args)
		m := base.Pinned("0.13.0-rc.2")
		if m.String() != tc.want {
			t.Errorf("Pinned = %q, want %q", m.String(), tc.want)
		}
		if !slices.Equal(base.Args, before) {
			t.Errorf("Pinned changed the original Args: %q", base.Args)
		}
	}
	m := DetectOn("linux", "/usr/local/bin/nearhost", getenv).Pinned("0.13.0-rc.2")
	if !slices.Contains(m.Env, "NEARHOST_VERSION=v0.13.0-rc.2") || !slices.Contains(m.Env, "NEARHOST_INSTALL_DIR=/usr/local/bin") {
		t.Errorf("Env = %q", m.Env)
	}
	w := DetectOn("windows", `C:\Tools\nearhost.exe`, getenv).Pinned("v0.13.0-rc.2")
	if want := `$env:NEARHOST_VERSION='v0.13.0-rc.2'; irm https://nearhost.dev/install.ps1 | iex`; w.String() != want {
		t.Errorf("windows Pinned = %q, want %q", w.String(), want)
	}
	if !slices.Equal(w.Env, []string{`NEARHOST_INSTALL_DIR=C:\Tools`, "NEARHOST_NO_PATH=1", "NEARHOST_VERSION=v0.13.0-rc.2"}) {
		t.Errorf("Env = %q, want NEARHOST_VERSION set once, to the version", w.Env)
	}
	if again := w.Pinned("0.13.0-rc.3"); !slices.Equal(again.Env, []string{`NEARHOST_INSTALL_DIR=C:\Tools`, "NEARHOST_NO_PATH=1", "NEARHOST_VERSION=v0.13.0-rc.3"}) {
		t.Errorf("Pinned twice: Env = %q", again.Env)
	}
	for _, exe := range []string{"/opt/homebrew/Caskroom/nearhost/0.5.0/nearhost", "/Users/me/.npm/_npx/a/node_modules/@nearhost/cli-darwin-arm64/bin/nearhost"} {
		base := DetectOn("darwin", exe, getenv)
		if got := base.Pinned("0.13.0-rc.2"); !slices.Equal(got.Args, base.Args) || got.String() != base.String() {
			t.Errorf("Pinned changed %v", base.Kind)
		}
	}
}

// PowerShell ends a single-quoted string on any of five quote marks, and
// reads each one doubled as itself.
func TestPSQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`C:\Tools`, `C:\Tools`},
		{`C:\Users\José Núñez\bin`, `C:\Users\José Núñez\bin`},
		{`C:\Users\Seán O'Brien\bin`, `C:\Users\Seán O''Brien\bin`},
		{"C:\\Users\\Seán O\u2019Brien\\bin", "C:\\Users\\Seán O\u2019\u2019Brien\\bin"},
		{"\u2018\u2019\u201a\u201b'", "\u2018\u2018\u2019\u2019\u201a\u201a\u201b\u201b''"},
		{`say "hi" $x`, `say "hi" $x`},
	} {
		if got := PSQuote(tc.in); got != tc.want {
			t.Errorf("PSQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
