package update

import (
	"path"
	"runtime"
	"slices"
	"strings"

	"github.com/Masterminds/semver/v3"
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

// scriptRun is the command nearhost runs for a script install. It downloads
// the installer to a temporary file and runs it only when curl has fetched
// all of it, so a download that fails part-way runs nothing and the
// command fails with curl's status, which `curl | sh` would hide behind
// sh's. The file goes when the shell exits; the trap is in single quotes,
// so $t is read then and not parsed again, whatever TMPDIR holds.
// NEARHOST_INSTALL_DIR and
// NEARHOST_VERSION reach the installer through the environment (Env),
// never through this text, so no directory name can change the command.
const scriptRun = `t=$(mktemp) && trap 'rm -f "$t"' EXIT && curl -fsSL https://nearhost.dev/install.sh -o "$t" && sh "$t"`

// InstallScriptWindows is the PowerShell installer's one-liner.
const InstallScriptWindows = "irm https://nearhost.dev/install.ps1 | iex"

// psQuotes doubles each character that ends a single-quoted PowerShell
// string: the apostrophe and the four curly single quotes, U+2018 to U+201B.
// PowerShell reads each one doubled as itself.
var psQuotes = strings.NewReplacer(
	"'", "''",
	"\u2018", "\u2018\u2018",
	"\u2019", "\u2019\u2019",
	"\u201a", "\u201a\u201a",
	"\u201b", "\u201b\u201b",
)

// PSQuote is s for the inside of a single-quoted PowerShell string.
func PSQuote(s string) string { return psQuotes.Replace(s) }

// Method is how to update a copy of nearhost.
type Method struct {
	Kind Kind
	// Args is the command that installs the latest release, or nil when
	// there is nothing to run: an npx copy, or a Windows copy from the
	// PowerShell installer or a zip, which nearhost replaces itself.
	Args []string
	// Env is added to the command's environment.
	Env []string
	// Dir is the directory the binary is in.
	Dir string
}

// envValue is the value of name in Env, or "".
func (m Method) envValue(name string) string {
	for _, e := range m.Env {
		if v, ok := strings.CutPrefix(e, name+"="); ok {
			return v
		}
	}
	return ""
}

// String is the command as the user would type it, or "" when there is none.
func (m Method) String() string {
	if m.Args == nil {
		return ""
	}
	if m.Kind == Script {
		v := m.envValue("NEARHOST_VERSION")
		if v != "" {
			return "curl -fsSL https://nearhost.dev/install.sh | NEARHOST_VERSION=" + v + " sh"
		}
		return InstallScript
	}
	return strings.Join(m.Args, " ")
}

// SameVersion reports whether installed, the version a binary reports, is
// target. A leading v and build metadata (+...) do not count; anything that
// is not a version, such as a development build's "dev", is never target.
func SameVersion(installed, target string) bool {
	i, err := semver.NewVersion(installed)
	if err != nil {
		return false
	}
	t, err := semver.NewVersion(target)
	return err == nil && i.Equal(t)
}

// Pinned is the method installing exactly version rather than the latest
// release, which a pre-release build needs because "latest" never means a
// pre-release. Homebrew and npx copies are unchanged.
func (m Method) Pinned(version string) Method {
	version = strings.TrimPrefix(version, "v")
	out := m
	out.Args = slices.Clone(m.Args)
	out.Env = slices.Clone(m.Env)
	switch m.Kind {
	case NPM, PNPM, Bun:
		out.Args[len(out.Args)-1] = "@nearhost/cli@" + version
	case Go:
		out.Args[len(out.Args)-1] = "github.com/nearhost/nearhost/cmd/nearhost@v" + version
	case Script:
		out.Env = slices.DeleteFunc(out.Env, func(e string) bool { return strings.HasPrefix(e, "NEARHOST_VERSION=") })
		out.Env = append(out.Env, "NEARHOST_VERSION=v"+version)
	}
	return out
}

// Detect works out from exe, the running binary's path with symlinks
// resolved, how it was installed. getenv reads the environment.
func Detect(exe string, getenv func(string) string) Method {
	return DetectOn(runtime.GOOS, exe, getenv)
}

// DetectOn is Detect for a binary on the operating system goos, whatever
// system the code runs on, so a Windows path is read with Windows rules.
func DetectOn(goos, exe string, getenv func(string) string) Method {
	windows := goos == "windows"
	dir := path.Dir(exe)
	slashed := exe
	if windows {
		dir = winDir(exe)
		slashed = strings.ToLower(strings.ReplaceAll(exe, `\`, "/")) // Windows paths ignore case
	}
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
	case goBin(dir, getenv, goos):
		m.Kind, m.Args = Go, []string{"go", "install", "github.com/nearhost/nearhost/cmd/nearhost@latest"}
	case windows:
		// The PowerShell installer or a zip. There is no command to run:
		// nearhost fetches the release itself (Downloader).
		m.Kind = Script
	default:
		m.Kind = Script
		m.Args = []string{"sh", "-c", scriptRun}
		m.Env = []string{"NEARHOST_INSTALL_DIR=" + dir}
	}
	return m
}

// winDir is the directory of a Windows path, whichever system this runs on.
func winDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return "."
	}
	if i == 2 && p[1] == ':' { // C:\nearhost.exe is in C:\
		return p[:3]
	}
	return p[:i]
}

// winPath is p in one spelling, to compare Windows paths: backslashes, no
// trailing separator.
func winPath(p string) string {
	return strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
}

// goBin reports whether dir is where `go install` puts binaries on the
// operating system goos: GOBIN, or the bin directory of the first GOPATH
// entry, which defaults to ~/go, or %USERPROFILE%\go on Windows whatever
// HOME says.
func goBin(dir string, getenv func(string) string, goos string) bool {
	if goos == "windows" {
		return goBinWindows(dir, getenv)
	}
	if gobin := getenv("GOBIN"); gobin != "" {
		return path.Clean(gobin) == dir
	}
	gopath := getenv("GOPATH")
	if gopath == "" {
		home := getenv("HOME")
		if home == "" {
			return false
		}
		gopath = path.Join(home, "go")
	}
	first, _, _ := strings.Cut(gopath, ":")
	return path.Join(first, "bin") == dir
}

func goBinWindows(dir string, getenv func(string) string) bool {
	same := func(a, b string) bool { return strings.EqualFold(winPath(a), winPath(b)) }
	if gobin := getenv("GOBIN"); gobin != "" {
		return same(gobin, dir)
	}
	gopath := getenv("GOPATH")
	if gopath == "" {
		home := getenv("USERPROFILE")
		if home == "" {
			return false
		}
		gopath = winPath(home) + `\go`
	}
	first, _, _ := strings.Cut(gopath, ";") // a Windows path list is split on ;
	return same(winPath(first)+`\bin`, dir)
}
