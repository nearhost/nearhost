package update

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	home := map[string]string{"HOME": "/Users/me"}
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
			m := Detect(tc.exe, env(tc.env))
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
	m := Detect("/opt/tools/bin/nearhost", func(string) string { return "" })
	if !slices.Contains(m.Env, "NEARHOST_INSTALL_DIR=/opt/tools/bin") {
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
		{"windows", filepath.Join("/u", "go", "bin"), true},
		{"windows", filepath.Join("/h", "go", "bin"), false},
		{"darwin", filepath.Join("/h", "go", "bin"), true},
		{"linux", filepath.Join("/u", "go", "bin"), false},
	} {
		if got := goBin(tc.dir, getenv, tc.goos); got != tc.want {
			t.Errorf("goBin(%s) on %s = %v, want %v", tc.dir, tc.goos, got, tc.want)
		}
	}
}
