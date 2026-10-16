package update

import (
	"path/filepath"
	"strings"
)

// Kind is how a copy of nearhost was installed.
type Kind int

const (
	// Script is the shell installer, and anything not recognised.
	Script Kind = iota
	Homebrew
	NPM
	PNPM
	Bun
	// Ephemeral is a copy npx or bunx fetched for one run.
	Ephemeral
	Go
)

func (k Kind) String() string {
	return [...]string{"the install script", "Homebrew", "npm", "pnpm", "bun", "npx", "go install"}[k]
}

// InstallScript is the shell installer's one-liner.
const InstallScript = "curl -fsSL https://nearhost.dev/install.sh | sh"

// Method is how to update a copy of nearhost.
type Method struct {
	Kind Kind
	// Args is the command that installs the latest release, or nil when
	// there is nothing to run.
	Args []string
	// Env is added to the command's environment.
	Env []string
	// Dir is the directory the binary is in.
	Dir string
}

// String is the command as the user would type it, or "" when there is none.
func (m Method) String() string {
	if m.Args == nil {
		return ""
	}
	if m.Kind == Script {
		return InstallScript
	}
	return strings.Join(m.Args, " ")
}

// Detect works out from exe, the running binary's path with symlinks
// resolved, how it was installed. getenv reads the environment.
func Detect(exe string, getenv func(string) string) Method {
	dir := filepath.Dir(exe)
	slashed := filepath.ToSlash(exe)
	m := Method{Dir: dir}
	switch {
	case strings.Contains(slashed, "/Caskroom/nearhost/"):
		m.Kind, m.Args = Homebrew, []string{"brew", "upgrade", "--cask", "nearhost/tap/nearhost"}
	case strings.Contains(slashed, "/node_modules/@nearhost/cli-"):
		switch {
		case strings.Contains(slashed, "/_npx/") || strings.Contains(slashed, "/bunx-") || strings.Contains(slashed, "/dlx"):
			m.Kind = Ephemeral
		case strings.Contains(slashed, "/.pnpm/") || strings.Contains(slashed, "/pnpm/"):
			m.Kind, m.Args = PNPM, []string{"pnpm", "add", "-g", "@nearhost/cli@latest"}
		case strings.Contains(slashed, "/.bun/"):
			m.Kind, m.Args = Bun, []string{"bun", "add", "-g", "@nearhost/cli@latest"}
		default:
			m.Kind, m.Args = NPM, []string{"npm", "install", "-g", "@nearhost/cli@latest"}
		}
	case goBin(dir, getenv):
		m.Kind, m.Args = Go, []string{"go", "install", "github.com/nearhost/nearhost/cmd/nearhost@latest"}
	default:
		m.Kind = Script
		m.Args = []string{"sh", "-c", InstallScript}
		m.Env = []string{"NEARHOST_INSTALL_DIR=" + dir}
	}
	return m
}

// goBin reports whether dir is where `go install` puts binaries: GOBIN, or
// the bin directory of the first GOPATH entry, which defaults to ~/go.
func goBin(dir string, getenv func(string) string) bool {
	if gobin := getenv("GOBIN"); gobin != "" {
		return filepath.Clean(gobin) == dir
	}
	gopath := getenv("GOPATH")
	if gopath == "" {
		home := getenv("HOME")
		if home == "" {
			return false
		}
		gopath = filepath.Join(home, "go")
	}
	first := filepath.SplitList(gopath)[0]
	return filepath.Join(first, "bin") == dir
}
