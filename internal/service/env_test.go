package service

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// pureResolve makes ServiceEnv judge the paths as given, whatever is on
// this machine's disk.
func pureResolve(t *testing.T) {
	before := resolve
	resolve = func(p string) (string, error) { return p, nil }
	t.Cleanup(func() { resolve = before })
}

func TestServiceEnv(t *testing.T) {
	pureResolve(t)
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	path := EnvVar{"PATH", "/bin"}
	tests := []struct {
		name    string
		goos    string
		getenv  func(string) string
		manager map[string]string
		want    []EnvVar
		note    string // substring of the only note, "" for none
	}{
		{"launchd socket", "darwin",
			env("PATH", "/bin", "SSH_AUTH_SOCK", "/private/tmp/com.apple.launchd.x/Listeners"), nil,
			[]EnvVar{path}, ""},
		{"1password socket", "darwin",
			env("PATH", "/bin", "SSH_AUTH_SOCK", "/Users/e/.1password/agent.sock"), nil,
			[]EnvVar{path, {"SSH_AUTH_SOCK", "/Users/e/.1password/agent.sock"}}, ""},
		{"runtime dir socket", "linux",
			env("PATH", "/bin", "SSH_AUTH_SOCK", "/run/user/1000/gcr/ssh"), nil,
			[]EnvVar{path, {"SSH_AUTH_SOCK", "/run/user/1000/gcr/ssh"}}, ""},
		{"forwarded socket", "linux",
			env("PATH", "/bin", "SSH_AUTH_SOCK", "/tmp/ssh-AbC/agent.42"), nil,
			[]EnvVar{path}, "/tmp/ssh-AbC/agent.42"},
		{"manager socket", "linux",
			env("PATH", "/bin", "SSH_AUTH_SOCK", "/tmp/x/sock"), map[string]string{"SSH_AUTH_SOCK": "/tmp/x/sock"},
			[]EnvVar{path}, ""},
		{"unset socket", "linux", env("PATH", "/bin"), nil, []EnvVar{path}, "SSH_AUTH_SOCK is not set"},
		{"display", "linux",
			env("PATH", "/bin", "DISPLAY", ":0", "WAYLAND_DISPLAY", "wayland-0", "SSH_AUTH_SOCK", "/run/user/1000/s"),
			map[string]string{"DISPLAY": ":0"},
			[]EnvVar{path, {"WAYLAND_DISPLAY", "wayland-0"}, {"SSH_AUTH_SOCK", "/run/user/1000/s"}}, ""},
		{"docker and xdg", "darwin",
			env("PATH", "/bin", "DOCKER_HOST", "unix:///d.sock", "DOCKER_CONTEXT", "c", "XDG_CONFIG_HOME", "/c", "XDG_DATA_HOME", "/d",
				"DISPLAY", ":0", "SSH_AUTH_SOCK", "/private/tmp/com.apple.launchd.a/b"), nil,
			[]EnvVar{path, {"DOCKER_HOST", "unix:///d.sock"}, {"DOCKER_CONTEXT", "c"}, {"XDG_CONFIG_HOME", "/c"}, {"XDG_DATA_HOME", "/d"}}, ""},
		{"dbus", "linux", env("PATH", "/bin", "DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus", "SSH_AUTH_SOCK", "/run/user/1000/s"),
			map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"},
			[]EnvVar{path, {"SSH_AUTH_SOCK", "/run/user/1000/s"}}, ""},
		{"dbus differs", "linux", env("PATH", "/bin", "DBUS_SESSION_BUS_ADDRESS", "unix:path=/x", "SSH_AUTH_SOCK", "/run/user/1000/s"),
			map[string]string{"DBUS_SESSION_BUS_ADDRESS": "unix:path=/run/user/1000/bus"},
			[]EnvVar{path, {"DBUS_SESSION_BUS_ADDRESS", "unix:path=/x"}, {"SSH_AUTH_SOCK", "/run/user/1000/s"}}, ""},
		{"empty path", "linux", env("SSH_AUTH_SOCK", "/run/user/1000/s"), nil, []EnvVar{{"SSH_AUTH_SOCK", "/run/user/1000/s"}}, ""},
		{"windows", "windows", env("PATH", `C:\bin`), nil, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, notes := ServiceEnv(tc.goos, tc.getenv, tc.manager, "/Users/e", "/run/user/1000")
			if !slices.Equal(got, tc.want) {
				t.Errorf("env = %v, want %v", got, tc.want)
			}
			switch {
			case tc.note == "" && len(notes) != 0:
				t.Errorf("notes = %q, want none", notes)
			case tc.note != "" && (len(notes) != 1 || !strings.Contains(notes[0].Error(), tc.note)):
				t.Errorf("notes = %q, want one containing %q", notes, tc.note)
			}
		})
	}
}

// A socket under the home folder that is a link to a forwarded agent's, as
// ~/.ssh/ssh_auth_sock is, goes when that login ends: it is judged by where
// it leads.
func TestServiceEnvFollowsALink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows service has no environment of its own")
	}
	home, forwarded := t.TempDir(), t.TempDir()
	target := filepath.Join(forwarded, "agent.42")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, ".ssh", "ssh_auth_sock")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	getenv := func(n string) string {
		return map[string]string{"PATH": "/bin", "SSH_AUTH_SOCK": link}[n]
	}
	env, notes := ServiceEnv(runtime.GOOS, getenv, nil, home, "")
	if slices.ContainsFunc(env, func(v EnvVar) bool { return v.Name == "SSH_AUTH_SOCK" }) {
		t.Errorf("env = %v, want no SSH_AUTH_SOCK", env)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Error(), "changes at each login") || !strings.Contains(notes[0].Error(), link) {
		t.Errorf("notes = %q, want one that says the socket changes at each login", notes)
	}

	// A link to a socket that lasts, under the home folder, is written.
	lasting := filepath.Join(home, ".1password", "agent.sock")
	if err := os.MkdirAll(filepath.Dir(lasting), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lasting, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(lasting, link); err != nil {
		t.Fatal(err)
	}
	env, notes = ServiceEnv(runtime.GOOS, getenv, nil, home, "")
	if !slices.Contains(env, EnvVar{"SSH_AUTH_SOCK", link}) || len(notes) != 0 {
		t.Errorf("env = %v, notes %q; want the link written, and no notes", env, notes)
	}
}

// A link whose forwarded agent has gone is still judged by where it leads,
// as it is while that agent lives.
func TestServiceEnvJudgesADanglingLinkByItsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a Windows service has no environment of its own")
	}
	home, forwarded := t.TempDir(), t.TempDir()
	link := filepath.Join(home, ".ssh", "ssh_auth_sock")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(forwarded, "gone", "agent.42"), link); err != nil {
		t.Fatal(err)
	}
	judge(t, link, home)

	// Through a chain of links, each followed to the end.
	middle := filepath.Join(home, ".ssh", "middle")
	if err := os.Rename(link, middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("middle", link); err != nil {
		t.Fatal(err)
	}
	judge(t, link, home)

	// A loop ends, and is judged where the following stopped.
	if err := os.Remove(middle); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("ssh_auth_sock", middle); err != nil {
		t.Fatal(err)
	}
	resolved(link)
}

func judge(t *testing.T, link, home string) {
	t.Helper()
	getenv := func(n string) string {
		return map[string]string{"PATH": "/bin", "SSH_AUTH_SOCK": link}[n]
	}
	env, notes := ServiceEnv(runtime.GOOS, getenv, nil, home, "")
	if slices.ContainsFunc(env, func(v EnvVar) bool { return v.Name == "SSH_AUTH_SOCK" }) {
		t.Errorf("env = %v, want no SSH_AUTH_SOCK", env)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Error(), "changes at each login") {
		t.Errorf("notes = %q, want one that says the socket changes at each login", notes)
	}
}

// Links are followed only when the service is for this machine: another
// system's paths mean nothing on this one's disk.
func TestServiceEnvFollowsLinksOnlyOnThisSystem(t *testing.T) {
	before := resolve
	t.Cleanup(func() { resolve = before })
	called := false
	resolve = func(p string) (string, error) {
		called = true
		if p == "/Users/e/.ssh/ssh_auth_sock" {
			return "/tmp/ssh-AbC/agent.42", nil
		}
		return p, nil
	}
	getenv := func(n string) string {
		return map[string]string{"PATH": "/bin", "SSH_AUTH_SOCK": "/Users/e/.ssh/ssh_auth_sock"}[n]
	}
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	env, notes := ServiceEnv(other, getenv, nil, "/Users/e", "")
	if called || !slices.Contains(env, EnvVar{"SSH_AUTH_SOCK", "/Users/e/.ssh/ssh_auth_sock"}) || len(notes) != 0 {
		t.Errorf("for %s: resolved %v, env %v, notes %q; want the path judged as given", other, called, env, notes)
	}
	if runtime.GOOS == "windows" {
		return
	}
	_, notes = ServiceEnv(runtime.GOOS, getenv, nil, "/Users/e", "")
	if !called || len(notes) != 1 || !strings.Contains(notes[0].Error(), "/tmp/ssh-AbC/agent.42") {
		t.Errorf("for this system: resolved %v, notes %q; want a note naming where the link leads", called, notes)
	}
}

// Each note is a message with a code of its own: a missing socket is a
// notice, one that changes at each login a warning.
func TestServiceEnvNotesAreMessages(t *testing.T) {
	pureResolve(t)
	_, notes := ServiceEnv("linux", func(string) string { return "" }, nil, "/home/u", "/run/user/1000")
	if len(notes) != 1 || notes[0].Code != report.ServiceAgentUnset || notes[0].Level != report.Info {
		t.Fatalf("notes %+v", notes)
	}
	getenv := func(n string) string { return map[string]string{"SSH_AUTH_SOCK": "/tmp/ssh-x/agent.1"}[n] }
	_, notes = ServiceEnv("linux", getenv, nil, "/home/u", "/run/user/1000")
	if len(notes) != 1 || notes[0].Code != report.ServiceAgentChanges || notes[0].Level != report.Warning {
		t.Fatalf("notes %+v", notes)
	}
	if len(notes[0].Fix) == 0 {
		t.Errorf("no step in %+v", notes[0])
	}
}
