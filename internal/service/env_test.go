package service

import (
	"slices"
	"strings"
	"testing"
)

func TestServiceEnv(t *testing.T) {
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
			case tc.note != "" && (len(notes) != 1 || !strings.Contains(notes[0], tc.note)):
				t.Errorf("notes = %q, want one containing %q", notes, tc.note)
			}
		})
	}
}
