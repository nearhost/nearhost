package cli

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
)

// noRemote is an add backend for tests that must stay on this machine.
func noRemote(t *testing.T) addBackend {
	return addBackend{openRemote: func(context.Context, config.Server, string) (discovery.FileReader, string, func() error, error) {
		t.Error("a local add opened a remote session")
		return nil, "", nil, errors.New("unexpected")
	}}
}

func TestAddDiscoversLocalProjectAndSavesConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	dir := fixture(t, "sail-custom")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "myapp", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, ok := loadSaved(t, cfgPath).Project("myapp")
	if !ok || p.Server != config.LocalServer || p.Alias != "myapp.test" || p.Path != dir || !p.Enabled {
		t.Fatalf("project = %+v", p)
	}
	if web, ok := p.WebRoute(); !ok || web.Port != 8080 {
		t.Fatalf("web route = %+v %v", web, ok)
	}
	want := "No preset registries are trusted. To use the official presets: nearhost preset registry add nearhost/presets\n" +
		"\n" +
		"Routes found for myapp (local:" + dir + "):\n" +
		"  PORT  ROLE    LABEL\n" +
		"  3307  ignore  forward-db (FORWARD_DB_PORT)\n" +
		"  5174  ignore  vite (VITE_PORT)\n" +
		"  8080  web     app (APP_PORT)\n" +
		"\n" +
		"Added myapp on local at https://myapp.test\n" +
		"Start it with `nearhost up`.\n"
	if out != want {
		t.Fatalf("add printed:\n%s\nwant:\n%s", out, want)
	}
}

func TestAddDefaultsToTheCurrentDirectoryAndItsName(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	dir := fixture(t, "sail-custom")
	t.Chdir(dir)
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", "--disabled", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, ok := loadSaved(t, cfgPath).Project("sail-custom")
	if !ok || p.Alias != "sail-custom.test" || p.Enabled {
		t.Fatalf("project = %+v %v", p, ok)
	}
	if got, _ := filepath.EvalSymlinks(p.Path); got != dir {
		t.Fatalf("path = %q, want %q", p.Path, dir)
	}
	wantContains(t, out, "Added sail-custom on local at https://sail-custom.test", "Start it with `nearhost up sail-custom`.")
}

func TestAddWebFlagOverridesPick(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "lit", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, _ := loadSaved(t, cfgPath).Project("lit")
	webs := 0
	for _, r := range p.Routes {
		if r.Role == config.RoleWeb {
			webs++
		}
	}
	if web, _ := p.WebRoute(); web.Port != 9000 || webs != 1 {
		t.Fatalf("expected 9000 as the only web route, got %+v", p.Routes)
	}
	wantContains(t, out, "  8080  ignore  app (APP_PORT)\n", "  9000  web     web (--web)\n")

	out, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "bad", "--web", "70000")
	if err == nil || !strings.Contains(err.Error(), "--web") {
		t.Fatalf("expected --web 70000 to be refused, got %v\n%s", err, out)
	}
}

func TestAddFailsWhenNoWebPortCanBePicked(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "empty"), "--name", "nothing")
	if err == nil || !strings.Contains(err.Error(), "--web <port>") || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("expected guidance about --web, got %v\n%s", err, out)
	}
	wantContains(t, out, "No ports found")
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Fatal("config must not be written on failure")
	}
}

func TestAddLocalPathMustBeADirectory(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("expected an error naming the missing path, got %v", err)
	}
	file := filepath.Join(fixture(t, "sail-custom"), "artisan")
	if _, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", file); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("expected a not-a-directory error, got %v", err)
	}
	if _, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", "a", "b"); err == nil {
		t.Fatal("add must reject a second path")
	}
}

// existing is a config with one remote server and one enabled local project
// that forwards loopback port 5174 (as the sail-custom fixture does).
func existing(t *testing.T) (path string, before []byte) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, path, &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini.lan"}},
		Projects: []config.Project{{
			Name: "shop", Server: config.LocalServer, Path: "/srv/shop", Alias: "shop.test", Enabled: true,
			Routes: []config.Route{{Port: 8000, Role: config.RoleWeb}, {Port: 5174, Role: config.RoleLoopback}},
		}},
	})
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, before
}

func TestAddUsesEnabledPresets(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	enablePresets(t, cfgPath, "stack", "bundler")
	dir := fixture(t, "sail-custom")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "myapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	want := "Routes found for myapp (local:" + dir + "):\n" +
		"  PORT  ROLE      LABEL\n" +
		"  1025  ignore    mailpit (FORWARD_MAILPIT_PORT)\n" +
		"  3307  ignore    Database (FORWARD_DB_PORT)\n" +
		"  5174  loopback  Assets (VITE_PORT)\n" +
		"  6379  ignore    redis (FORWARD_REDIS_PORT)\n" +
		"  8025  ignore    mailpit (FORWARD_MAILPIT_DASHBOARD_PORT)\n" +
		"  8080  web       App (APP_PORT)\n" +
		"\n" +
		"Advisory (app-url): APP_URL should be https://myapp.test.\n" +
		"  APP_URL=https://myapp.test\n" +
		"\n" +
		"Added myapp on local at https://myapp.test\n" +
		"Start it with `nearhost up`.\n"
	if out != want {
		t.Fatalf("add printed:\n%s\nwant:\n%s", out, want)
	}
	saved := loadSaved(t, cfgPath)
	if len(saved.Presets) != 2 {
		t.Errorf("adding a project lost the enabled presets: %+v", saved.Presets)
	}
}

// A preset that cannot be used is reported and the project is still added.
func TestAddCarriesOnPastABrokenPreset(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{Presets: []config.PresetRef{{Name: "gone"}}})
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, `Warning: preset "gone" is enabled but`, "  8080  web     app (APP_PORT)\n", "Added myapp on local")
}

func TestAddRefusesALoopbackClashAndSavesNothing(t *testing.T) {
	cfgPath, _ := existing(t)
	enablePresets(t, cfgPath, "stack", "bundler")
	before, _ := os.ReadFile(cfgPath)
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "second")
	if err == nil {
		t.Fatalf("add succeeded:\n%s", out)
	}
	for _, want := range []string{"loopback port 5174", `"shop"`, "--disabled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Errorf("config changed:\n%s", after)
	}
}

func TestAddRefusesWhatWouldNotValidateAndSavesNothing(t *testing.T) {
	dir := fixture(t, "compose-literal")
	cases := []struct {
		name string
		args []string
		want []string // substrings of the error
	}{
		{"duplicate name", []string{"add", dir, "--name", "shop", "--web", "9000"}, []string{`project "shop" already exists`, "--name"}},
		{"duplicate alias", []string{"add", dir, "--name", "Shop", "--web", "9000"}, []string{"shop.test", `"shop"`, "--alias"}},
		{"duplicate alias given", []string{"add", dir, "--name", "other", "--alias", "shop.test", "--web", "9000"}, []string{"shop.test", "--alias"}},
		{"invalid alias", []string{"add", dir, "--name", "other", "--alias", "Other.example", "--web", "9000"}, []string{"Other.example", "lowercase domain name"}},
		{"alias is an IP address", []string{"add", dir, "--name", "other", "--alias", "127.0.0.1", "--web", "9000"}, []string{"127.0.0.1", "IP address"}},
		{"unknown server", []string{"add", "/srv/x", "--server", "nas", "--name", "x"}, []string{`unknown server "nas"`, "servers:"}},
		{"remote without a path", []string{"add", "--server", "mini", "--name", "x"}, []string{"path"}},
		{"remote with a relative path", []string{"add", "code/x", "--server", "mini", "--name", "x"}, []string{"absolute path"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath, before := existing(t)
			out, err := execute(t, newAddCmd(noRemote(t)), append([]string{"--config", cfgPath}, tc.args...)...)
			if err == nil {
				t.Fatalf("add succeeded:\n%s", out)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
			if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
				t.Errorf("config changed:\n%s", after)
			}
		})
	}
}

func TestAddLoopbackClashIsAllowedWhenDisabled(t *testing.T) {
	cfgPath, _ := existing(t)
	enablePresets(t, cfgPath, "stack", "bundler")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "second", "--disabled")
	if err != nil {
		t.Fatalf("add --disabled: %v\n%s", err, out)
	}
	wantContains(t, out, "Advisory (loopback-clash): Port 5174 is also forwarded to this machine by project \"shop\".", "  VITE_PORT=5175\n")
	if p, ok := loadSaved(t, cfgPath).Project("second"); !ok || p.Enabled {
		t.Fatalf("project = %+v %v", p, ok)
	}
}

func TestAddRefusesToTouchAnInvalidConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{Projects: []config.Project{{Name: "broken", Server: "gone", Alias: "broken.test"}}})
	before, _ := os.ReadFile(cfgPath)
	_, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "myapp")
	if err == nil || !strings.Contains(err.Error(), cfgPath) || !strings.Contains(err.Error(), `unknown server "gone"`) {
		t.Fatalf("expected the config's own error, got %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatalf("config changed:\n%s", after)
	}
}

func TestAddRemoteReadsTheProjectThroughTheServerSession(t *testing.T) {
	cfgPath, _ := existing(t)
	var gotServer config.Server
	var gotPath string
	closed := 0
	b := addBackend{openRemote: func(_ context.Context, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
		gotServer, gotPath = srv, projectPath
		return discovery.DirReader{Root: fixture(t, "compose-literal")}, projectPath, func() error { closed++; return nil }, nil
	}}
	out, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "mini", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if gotServer != (config.Server{Name: "mini", SSHHost: "mini.lan"}) || gotPath != "/srv/www/blog" || closed != 1 {
		t.Fatalf("openRemote got %+v %q, closed %d times", gotServer, gotPath, closed)
	}
	p, ok := loadSaved(t, cfgPath).Project("blog")
	if !ok || p.Server != "mini" || p.Path != "/srv/www/blog" || p.Alias != "blog.test" {
		t.Fatalf("project = %+v %v", p, ok)
	}
	wantContains(t, out, "Routes found for blog (mini:/srv/www/blog):", "Added blog on mini at https://blog.test")
}

// sessionReader reads from memory until its session is closed, as an SFTP
// client does.
type sessionReader struct {
	files  map[string]string
	closed bool
}

func (s *sessionReader) ReadFile(name string) ([]byte, error) {
	if s.closed {
		return nil, errors.New("use of closed network connection")
	}
	data, ok := s.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(data), nil
}

// An advisory may read a file that discovery never did. The session must
// still be open when it does.
func TestAddRemoteAdvisoryReadsThroughAnOpenSession(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{
		Servers: []config.Server{{Name: "mini", SSHHost: "mini.lan"}},
		Presets: []config.PresetRef{{Name: "marker"}},
	})
	dir := filepath.Join(filepath.Dir(cfgPath), "presets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "schema: 1\nname: marker\ntitle: Marker\ndetect: 'true'\nadvisories:\n  - {id: marker, when: 'file(\"marker\")', message: The marker is there.}\n"
	if err := os.WriteFile(filepath.Join(dir, "marker.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r := &sessionReader{files: map[string]string{"marker": "1"}}
	b := addBackend{openRemote: func(context.Context, config.Server, string) (discovery.FileReader, string, func() error, error) {
		return r, "/srv/www/blog", func() error { r.closed = true; return nil }, nil
	}}
	out, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "mini", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "Advisory (marker): The marker is there.\n")
	if !r.closed {
		t.Error("the session was left open")
	}
}

func TestAddRemoteFailureSavesNothing(t *testing.T) {
	cfgPath, before := existing(t)
	b := addBackend{openRemote: func(context.Context, config.Server, string) (discovery.FileReader, string, func() error, error) {
		return nil, "", nil, errors.New("ssh mini.lan failed: Permission denied (publickey).")
	}}
	_, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "mini")
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey).") {
		t.Fatalf("expected the ssh error, got %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatalf("config changed:\n%s", after)
	}
}

// pflag takes a back-quoted word in a flag's usage as the name of its value,
// which turned --disabled into a flag that seemed to want an argument.
func TestAddHelpShowsDisabledAsASwitch(t *testing.T) {
	out, err := execute(t, newAddCmd(noRemote(t)), "add", "--help")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "      --disabled        ", "      --web int         ", "      --server string   ")
}

// The hint is a command to copy: a name that is not one plain word is quoted,
// and one that starts with a dash is kept from reading as a flag.
func TestAddQuotesTheProjectNameInTheStartHint(t *testing.T) {
	for name, want := range map[string]string{
		"my app":  "Start it with `nearhost up 'my app'`.\n",
		"it's":    `Start it with ` + "`" + `nearhost up 'it'\''s'` + "`.\n",
		"-x":      "Start it with `nearhost up -- -x`.\n",
		"shop.v2": "Start it with `nearhost up shop.v2`.\n",
	} {
		cfgPath := filepath.Join(t.TempDir(), "config.yaml")
		out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name="+name, "--alias", "x.test", "--disabled", "--web", "8080")
		if err != nil {
			t.Fatalf("add %q: %v\n%s", name, err, out)
		}
		if !strings.HasSuffix(out, want) {
			t.Errorf("add %q printed\n%s\nwant it to end with %s", name, out, want)
		}
	}
}

// --web can turn a port that was found as a loopback forward into the web
// route. The advisories are about the routes the project ends up with.
func TestAddAdvisesOnTheRolesAfterTheWebFlag(t *testing.T) {
	cfgPath, _ := existing(t)
	enablePresets(t, cfgPath, "stack", "bundler")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "second", "--web", "5174")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "  5174  web   ")
	if strings.Contains(out, "loopback-clash") {
		t.Errorf("advised on a loopback clash for a port that is the web route:\n%s", out)
	}
}

// The hosts file gets an alias without the blanks around it, so the config
// must hold the same: the proxy serves the alias exactly as configured.
func TestAddTrimsTheAlias(t *testing.T) {
	cfgPath, _ := existing(t)
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "padded", "--alias", " padded.test\t", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if p, _ := loadSaved(t, cfgPath).Project("padded"); p.Alias != "padded.test" {
		t.Errorf("saved the alias %q", p.Alias)
	}
	wantContains(t, out, "at https://padded.test\n")

	// Blanks do not hide a duplicate either.
	out, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "again", "--alias", "shop.test ", "--web", "8080")
	if err == nil || !strings.Contains(err.Error(), `already used by project "shop"`) {
		t.Errorf("a padded duplicate alias: %v\n%s", err, out)
	}
}

// A main name typed with --alias is approved by being typed, as --host is, so
// a real domain needs neither a prompt nor a terminal.
func TestAddAliasFlagApprovesRealDomain(t *testing.T) {
	cfgPath, _ := existing(t)
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "real", "--alias", "app.local.x.io", "--web", "8080")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if p, _ := loadSaved(t, cfgPath).Project("real"); p.Alias != "app.local.x.io" {
		t.Errorf("saved the alias %q", p.Alias)
	}
	wantContains(t, out, "at https://app.local.x.io\n")
}

func TestAddRefusesWildcardAlias(t *testing.T) {
	cfgPath, _ := existing(t)
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "wild", "--alias", "*.x.io", "--web", "8080")
	want := `invalid main name "*.x.io": must be a lowercase domain name, not a wildcard, localhost or an IP address`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// Advice can carry values from the project, such as its .env, and those
// must not reach the terminal as control characters.
func TestAddMakesRenderedAdviceSafeToPrint(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{Presets: []config.PresetRef{{Name: "echo"}}})
	dir := filepath.Join(filepath.Dir(cfgPath), "presets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "schema: 1\nname: echo\ntitle: Echo\ndetect: 'true'\nadvisories:\n" +
		"  - {id: echo, when: 'true', message: \"Name is {{ env.NAME }}.\\nSecond line.\", snippet: \"NAME={{ env.NAME }}\\n\\tindented\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "echo.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("NAME=a\x1b[2Jb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", project, "--name", "echo", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("output carries an escape character: %q", out)
	}
	wantContains(t, out, "Advisory (echo): Name is a[2Jb.\nSecond line.\n  NAME=a[2Jb\n  \tindented\n")
}

// envProject is a project directory holding only the given .env.
func envProject(t *testing.T, env string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// With no preset and no registry, the port variables in .env are routes.
func TestAddProposesEnvPortsWithoutPresets(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	dir := envProject(t, "APP_PORT=8080\nFORWARD_APP_PORT=8080\nREPORT_LIMIT=50\n")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "envapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "  8080  web   app (APP_PORT, FORWARD_APP_PORT)\n", "Added envapp on local")
	p, _ := loadSaved(t, cfgPath).Project("envapp")
	if len(p.Routes) != 1 || p.Routes[0].Label != "app (APP_PORT, FORWARD_APP_PORT)" {
		t.Fatalf("routes = %+v", p.Routes)
	}
}

// A project with only a Compose file has no routes until a preset asks for
// its published ports.
func TestAddReadsComposeOnlyThroughAPreset(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "plain", "--no-presets")
	if err == nil || !strings.Contains(err.Error(), "--web <port>") {
		t.Fatalf("expected no routes without a preset, got %v\n%s", err, out)
	}
	wantContains(t, out, "No port variables found in the .env files of plain (local:", ").\nIts Compose file is read only when a preset asks for it with compose: ports.\n")
	if strings.Contains(out, "No ports found") {
		t.Errorf("a Compose-only project is not one without ports:\n%s", out)
	}
	enablePresets(t, cfgPath, "compose")
	out, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "plain")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "  8080  web     web\n", "  8443  ignore  api\n", "  9000  ignore  api\n")
}

func TestAddRemoteNamesAProjectUnderTheServerHome(t *testing.T) {
	cfgPath, _ := existing(t)
	var gotPath string
	b := addBackend{openRemote: func(_ context.Context, _ config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
		gotPath = projectPath
		return discovery.DirReader{Root: fixture(t, "compose-literal")}, "/home/dev/code/blog", func() error { return nil }, nil
	}}
	out, err := execute(t, newAddCmd(b), "--config", cfgPath, "add", "~/code/blog/", "--server", "mini", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if gotPath != "~/code/blog" {
		t.Errorf("opened %q", gotPath)
	}
	if p, ok := loadSaved(t, cfgPath).Project("blog"); !ok || p.Path != "/home/dev/code/blog" {
		t.Fatalf("project = %+v %v", p, ok)
	}

	// The home directory itself has no name to give the project.
	_, err = execute(t, newAddCmd(b), "--config", cfgPath, "add", "~", "--server", "mini")
	if err == nil || !strings.Contains(err.Error(), "pass --name") {
		t.Fatalf("expected to be asked for a name, got %v", err)
	}
	// Other relative paths are still refused.
	_, err = execute(t, newAddCmd(b), "--config", cfgPath, "add", "code/blog", "--server", "mini")
	if err == nil || !strings.Contains(err.Error(), "absolute path") {
		t.Fatalf("expected a relative path to be refused, got %v", err)
	}
}

// rcProject is a project directory with APP_PORT=8220 in .env and the given
// .nearhostrc.
func rcProject(t *testing.T, rc string) string {
	t.Helper()
	dir := envProject(t, "APP_PORT=8220\n")
	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

const rcRealDomain = "version: 1\nroutes:\n  - port: ${APP_PORT}\n    hosts: [app.local.dd.io]\n"

// rcBackend is a backend for a project on this machine that can (or cannot) ask
// questions, and finds no address for any host.
func rcBackend(t *testing.T, terminal bool) addBackend {
	b := noRemote(t)
	b.interactive = func() bool { return terminal }
	return b
}

func TestAddReadsNearhostrcAndAsks(t *testing.T) {
	dir := rcProject(t, rcRealDomain)

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "rcapp", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "This project asks for these, which need your approval:", "app.local.dd.io on port 8220 (a real domain)", "Approve these? [y/N] ")
	p, ok := loadSaved(t, cfgPath).Project("rcapp")
	if !ok || len(p.Routes) == 0 {
		t.Fatalf("project = %+v %v", p, ok)
	}
	if r := p.Routes[0]; r.Port != 8220 || r.Role != config.RoleWeb || len(r.Hosts) != 1 || r.Hosts[0] != "app.local.dd.io" {
		t.Fatalf("first route = %+v", r)
	}
	if p.Nearhostrc == nil || p.Nearhostrc.SHA256 == "" {
		t.Fatalf("Nearhostrc = %+v", p.Nearhostrc)
	}

	declined := filepath.Join(t.TempDir(), "config.yaml")
	for _, answer := range []string{"\n", ""} {
		out, err = executeWithInput(t, answer, newAddCmd(rcBackend(t, true)), "--config", declined, "add", dir, "--name", "rcapp", "--no-presets")
		if err == nil || err.Error() != "not added: what this project asks for was not approved" {
			t.Fatalf("answer %q: err = %v\n%s", answer, err, out)
		}
	}
	if _, statErr := os.Stat(declined); !os.IsNotExist(statErr) {
		t.Fatalf("a declined add wrote the config: %v", statErr)
	}

	out, err = executeWithInput(t, "", newAddCmd(rcBackend(t, false)), "--config", declined, "add", dir, "--name", "rcapp", "--no-presets")
	if err == nil || !strings.Contains(err.Error(), "app.local.dd.io") || !strings.Contains(err.Error(), "run nearhost add in a terminal") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestAddWarnsWhenAHostResolvesOnTheInternet(t *testing.T) {
	dir := rcProject(t, rcRealDomain)
	b := rcBackend(t, true)
	b.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
	}
	out, err := executeWithInput(t, "n\n", newAddCmd(b), "--config", filepath.Join(t.TempDir(), "config.yaml"), "add", dir, "--no-presets")
	if err == nil {
		t.Fatalf("a declined add succeeded\n%s", out)
	}
	wantContains(t, out, "(a real domain, resolves to 203.0.113.7 on the internet)")
}

func TestAddHostFlagIsApprovedByTyping(t *testing.T) {
	dir := envProject(t, "APP_PORT=8080\n")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "typed", "--web", "3000", "--host", "api.local.x.io", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Approve these?") {
		t.Fatalf("asked about a typed host:\n%s", out)
	}
	p, _ := loadSaved(t, cfgPath).Project("typed")
	web, ok := p.WebRoute()
	if !ok || web.Port != 3000 || len(web.Hosts) != 1 || web.Hosts[0] != "api.local.x.io" {
		t.Fatalf("web route = %+v %v", web, ok)
	}

	out, err = execute(t, newAddCmd(noRemote(t)), "--config", filepath.Join(t.TempDir(), "config.yaml"), "add", dir, "--web", "3000", "--host", "*.com", "--no-presets")
	if err == nil || !strings.Contains(err.Error(), "*.com") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

func TestAddRefusesAHostAnotherProjectServes(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{Projects: []config.Project{{
		Name: "one", Server: config.LocalServer, Path: "/srv/one", Alias: "one.test", Enabled: true,
		Routes: []config.Route{{Port: 8000, Role: config.RoleWeb, Hosts: []string{"x.local.example.io"}}},
	}}})
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", envProject(t, ""), "--web", "3000", "--host", "x.local.example.io", "--no-presets")
	want := `host x.local.example.io is already served by project "one"`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q\n%s", err, want, out)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatalf("config changed:\n%s", after)
	}
}

func TestAddHostFlagApprovesAHostTheFileLists(t *testing.T) {
	dir := rcProject(t, rcRealDomain)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "rcapp", "--host", "app.local.dd.io", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Approve these?") {
		t.Fatalf("asked about a typed host:\n%s", out)
	}
	p, _ := loadSaved(t, cfgPath).Project("rcapp")
	if got := p.AllHosts(); strings.Count(strings.Join(got, " "), "app.local.dd.io") != 1 {
		t.Fatalf("hosts = %v", got)
	}
}

func TestAddHostFlagTypedTwiceIsServedOnce(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", envProject(t, ""), "--name", "twice", "--web", "3000", "--host", "a.local.x.io", "--host", "A.local.x.io", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, _ := loadSaved(t, cfgPath).Project("twice")
	if web, _ := p.WebRoute(); len(web.Hosts) != 1 || web.Hosts[0] != "a.local.x.io" {
		t.Fatalf("hosts = %v", web.Hosts)
	}
}

const rcDockerClients = "version: 1\ndocker_clients: true\nroutes:\n  - port: ${APP_PORT}\n    hosts: [app.test]\n"

// docker_clients is asked about like a host: listed at the prompt, and saved
// only on a yes.
func TestAddAsksBeforeDockerClients(t *testing.T) {
	dir := rcProject(t, rcDockerClients)

	declined := filepath.Join(t.TempDir(), "config.yaml")
	out, err := executeWithInput(t, "n\n", newAddCmd(rcBackend(t, true)), "--config", declined, "add", dir, "--name", "rcapp", "--no-presets")
	if err == nil {
		t.Fatalf("a declined add succeeded\n%s", out)
	}
	wantContains(t, out, "  docker_clients (lets local Docker containers reach this project)\n", "Approve these? [y/N] ")
	if _, statErr := os.Stat(declined); !os.IsNotExist(statErr) {
		t.Fatalf("a declined add wrote the config: %v", statErr)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if out, err = executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "rcapp", "--no-presets"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if p, ok := loadSaved(t, cfgPath).Project("rcapp"); !ok || !p.DockerClients() {
		t.Fatalf("project = %+v %v, want docker_clients", p, ok)
	}
}

func TestAddWithoutTerminalNamesDockerClients(t *testing.T) {
	dir := rcProject(t, rcDockerClients)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := executeWithInput(t, "", newAddCmd(rcBackend(t, false)), "--config", cfgPath, "add", dir, "--name", "rcapp", "--no-presets")
	if err == nil || err.Error() != "not added: this project asks for things that need your approval (docker_clients); run nearhost add in a terminal, or open nearhost" {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// Containers cannot reach Nearhost from a server, so the file's request is
// refused rather than approved.
func TestAddRefusesDockerClientsOnAServer(t *testing.T) {
	cfgPath, before := existing(t)
	dir := rcProject(t, rcDockerClients)
	b := addBackend{openRemote: func(_ context.Context, _ config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
		return discovery.DirReader{Root: dir}, projectPath, func() error { return nil }, nil
	}}
	out, err := executeWithInput(t, "y\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "mini", "--name", "blog", "--no-presets")
	want := `.nearhostrc: docker_clients works only for projects on this machine; containers can't reach Nearhost from server "mini"`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatalf("a refused add changed the config:\n%s", after)
	}
}

// A .nearhostrc's alias is the project's main name unless --alias gives one,
// and a real domain from the file is asked about like the file's hosts.
func TestAddTakesAliasFromNearhostrc(t *testing.T) {
	dir := rcProject(t, "version: 1\nalias: ${APP_HOST:-rc.test}\nroutes:\n  - port: ${APP_PORT}\n")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, err := execute(t, newAddCmd(rcBackend(t, false)), "--config", cfgPath, "add", dir, "--name", "rcapp", "--no-presets")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, _ := loadSaved(t, cfgPath).Project("rcapp")
	if p.Alias != "rc.test" || p.Nearhostrc == nil || p.Nearhostrc.Alias != "rc.test" {
		t.Fatalf("saved alias %q, snapshot %+v", p.Alias, p.Nearhostrc)
	}
	wantContains(t, out, "at https://rc.test\n")

	flagged := filepath.Join(t.TempDir(), "config.yaml")
	out, err = execute(t, newAddCmd(rcBackend(t, false)), "--config", flagged, "add", dir, "--name", "rcapp", "--alias", "mine.test", "--no-presets")
	if err != nil {
		t.Fatalf("add --alias: %v\n%s", err, out)
	}
	p, _ = loadSaved(t, flagged).Project("rcapp")
	if p.Alias != "mine.test" || p.Nearhostrc.Alias != "rc.test" {
		t.Fatalf("--alias: saved alias %q, snapshot %+v", p.Alias, p.Nearhostrc)
	}

	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("APP_PORT=8220\nAPP_HOST=app.local.x.io\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(t.TempDir(), "config.yaml")
	out, err = executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", real, "add", dir, "--name", "rcapp", "--no-presets")
	if err != nil {
		t.Fatalf("add a real domain: %v\n%s", err, out)
	}
	wantContains(t, out, "This project asks for these, which need your approval:", "app.local.x.io on port 8220 (a real domain)", "Approve these? [y/N] ")
	if p, _ := loadSaved(t, real).Project("rcapp"); p.Alias != "app.local.x.io" {
		t.Fatalf("saved alias %q", p.Alias)
	}

	// The file's alias must be free, as a typed one must.
	saveConfig(t, real, &config.Config{Projects: []config.Project{{Name: "other", Server: config.LocalServer, Path: "/o", Alias: "app.local.x.io", Routes: []config.Route{{Port: 9000, Role: config.RoleWeb}}}}})
	out, err = executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", real, "add", dir, "--name", "rcapp", "--no-presets")
	if err == nil || !strings.Contains(err.Error(), `already used by project "other"`) {
		t.Fatalf("a taken file alias: %v\n%s", err, out)
	}
}

// enableHostedPreset installs the schema 2 preset that infers a web host from
// APP_URL, beside the config, and enables it.
func enableHostedPreset(t *testing.T, cfgPath string) {
	t.Helper()
	enableTestPreset(t, cfgPath, filepath.Join("testdata", "presets-hosts"), "hosted")
}

// enableReverbPreset installs the schema 2 preset with an App route on
// APP_PORT and a Reverb route whose host comes from REVERB_URL, beside the
// config, and enables it.
func enableReverbPreset(t *testing.T, cfgPath string) {
	t.Helper()
	enableTestPreset(t, cfgPath, filepath.Join("testdata", "presets-reverb"), "reverb")
}

// enableTestPreset installs the preset name from dir beside the config and
// enables it.
func enableTestPreset(t *testing.T, cfgPath, dir, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(presetsDir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(presetPath(cfgPath, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadSaved(t, cfgPath)
	cfg.UpsertPreset(config.PresetRef{Name: name})
	off := false
	cfg.RegistryPrompt = &off
	saveConfig(t, cfgPath, cfg)
}

const hostedEnv = "APP_URL=https://app.local.x.io:8220\nAPP_PORT=8220\n"

func TestAddNearhostrcBeatsInferredHosts(t *testing.T) {
	dir := envProject(t, hostedEnv)
	rc := "version: 1\nroutes:\n  - port: ${APP_PORT}\n    hosts: [auth.local.x.io]\n"
	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableHostedPreset(t, cfgPath)
	out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "rcapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "auth.local.x.io on port 8220")
	if strings.Contains(out, "app.local.x.io on port") {
		t.Errorf("the inferred host was asked about:\n%s", out)
	}
	p, _ := loadSaved(t, cfgPath).Project("rcapp")
	found := false
	for _, r := range p.Routes {
		if r.Port == 8220 {
			found = true
			if !reflect.DeepEqual(r.Hosts, []string{"auth.local.x.io"}) {
				t.Errorf("route 8220 hosts = %v", r.Hosts)
			}
		}
	}
	if !found {
		t.Fatalf("no route on 8220: %+v", p.Routes)
	}
}

func TestAddApprovesInferredHosts(t *testing.T) {
	dir := envProject(t, hostedEnv)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableHostedPreset(t, cfgPath)
	out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "hostapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "This project asks for these, which need your approval:", "app.local.x.io on port 8220 (a real domain)")

	other := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, other, &config.Config{})
	enableHostedPreset(t, other)
	_, err = execute(t, newAddCmd(rcBackend(t, false)), "--config", other, "add", dir, "--name", "hostapp")
	if err == nil || err.Error() != "not added: this project asks for things that need your approval (app.local.x.io); run nearhost add in a terminal, or open nearhost" {
		t.Fatalf("err = %v", err)
	}

	typed := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, typed, &config.Config{})
	enableHostedPreset(t, typed)
	out, err = execute(t, newAddCmd(rcBackend(t, false)), "--config", typed, "add", dir, "--name", "hostapp", "--host", "app.local.x.io")
	if err != nil {
		t.Fatalf("add --host: %v\n%s", err, out)
	}
	if strings.Contains(out, "Approve these?") {
		t.Errorf("--host still prompted:\n%s", out)
	}
}

func TestForceWebKeepsHostedRoutes(t *testing.T) {
	in := []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
		{Port: 6001, Role: config.RoleWeb, Hosts: []string{"ws.x.io"}},
		{Port: 3306, Role: config.RoleIgnore},
		{Port: 9000, Role: config.RoleWeb},
	}
	got := forceWeb(in, 6001)
	want := []config.Route{
		{Port: 6001, Role: config.RoleWeb, Hosts: []string{"ws.x.io"}},
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}},
		{Port: 3306, Role: config.RoleIgnore},
		{Port: 9000, Role: config.RoleIgnore},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("forceWeb = %+v\nwant %+v", got, want)
	}
	added := forceWeb([]config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io"}}, {Port: 3306, Role: config.RoleIgnore}}, 4000)
	if len(added) != 3 || added[0].Port != 4000 || added[0].Role != config.RoleWeb || added[1].Port != 80 || added[1].Role != config.RoleWeb {
		t.Errorf("added = %+v", added)
	}
}

func TestAddDecliningInferredHostsSavesNothing(t *testing.T) {
	dir := envProject(t, hostedEnv)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableHostedPreset(t, cfgPath)
	_, err := executeWithInput(t, "n\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "hostapp")
	if err == nil || err.Error() != "not added: what this project asks for was not approved" {
		t.Fatalf("err = %v", err)
	}
	if _, ok := loadSaved(t, cfgPath).Project("hostapp"); ok {
		t.Error("the project was saved")
	}
}

// reverbEnv names the alias rapp.test as the Reverb route's host, as a Laravel
// app whose REVERB_HOST is its own name does.
const reverbEnv = "APP_PORT=8220\nREVERB_PORT=8230\nREVERB_URL=https://rapp.test\n"

// wantAliasHostDropped fails unless routes have 8220 web and 8230 ignored
// without hosts: the alias is served on the primary, so the Reverb route
// has nothing of its own to serve.
func wantAliasHostDropped(t *testing.T, routes []config.Route) {
	t.Helper()
	found := 0
	for _, r := range routes {
		switch r.Port {
		case 8220:
			found++
			if r.Role != config.RoleWeb {
				t.Errorf("route 8220 = %+v, want web", r)
			}
		case 8230:
			found++
			if r.Role != config.RoleIgnore || len(r.Hosts) != 0 {
				t.Errorf("route 8230 = %+v, want ignore without hosts", r)
			}
		}
	}
	if found != 2 {
		t.Errorf("routes = %+v, want 8220 and 8230", routes)
	}
}

func TestAddDropsTheAliasFromSecondaryRoutes(t *testing.T) {
	dir := envProject(t, reverbEnv)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableReverbPreset(t, cfgPath)
	out, err := execute(t, newAddCmd(rcBackend(t, false)), "--config", cfgPath, "add", dir, "--name", "rapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	p, _ := loadSaved(t, cfgPath).Project("rapp")
	wantAliasHostDropped(t, p.Routes)
}

// Two checkouts of one app infer the same host. The second is added without
// it, saying why, rather than failed; a host typed with --host still fails.
func TestAddSkipsInferredHostsAnotherProjectServes(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableHostedPreset(t, cfgPath)
	if out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", envProject(t, hostedEnv), "--name", "first"); err != nil {
		t.Fatalf("first add: %v\n%s", err, out)
	}

	var stdout, stderr syncBuffer
	root := newRootCmd()
	root.AddCommand(newAddCmd(rcBackend(t, true)))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader("y\n"))
	root.SetArgs([]string{"--config", cfgPath, "add", envProject(t, hostedEnv), "--name", "second"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("second add: %v\n%s%s", err, stdout.String(), stderr.String())
	}
	if want := "Not serving app.local.x.io: project \"first\" already serves it.\n"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
	if strings.Contains(stdout.String(), "Approve these?") {
		t.Errorf("asked about the host:\n%s", stdout.String())
	}
	p, ok := loadSaved(t, cfgPath).Project("second")
	if !ok {
		t.Fatal("second was not saved")
	}
	if hosts := p.AllHosts(); !reflect.DeepEqual(hosts, []string{"second.test"}) {
		t.Errorf("second serves %v", hosts)
	}

	out, err := execute(t, newAddCmd(rcBackend(t, false)), "--config", cfgPath, "add", envProject(t, hostedEnv), "--name", "third", "--host", "app.local.x.io")
	if err == nil || err.Error() != `host app.local.x.io is already served by project "first"` {
		t.Fatalf("typed host: %v\n%s", err, out)
	}
}

// A disabled project serves nothing, so it keeps an inferred host another
// project serves; the clash comes up only when it is enabled.
func TestAddDisabledKeepsInferredHostsAnotherProjectServes(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableHostedPreset(t, cfgPath)
	if out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", envProject(t, hostedEnv), "--name", "first"); err != nil {
		t.Fatalf("first add: %v\n%s", err, out)
	}
	out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", envProject(t, hostedEnv), "--name", "second", "--disabled")
	if err != nil {
		t.Fatalf("second add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Not serving") {
		t.Errorf("left out a host:\n%s", out)
	}
	p, ok := loadSaved(t, cfgPath).Project("second")
	if !ok {
		t.Fatal("second was not saved")
	}
	if hosts := p.AllHosts(); !reflect.DeepEqual(hosts, []string{"second.test", "app.local.x.io"}) {
		t.Errorf("second serves %v", hosts)
	}
}

func TestWithoutTakenHosts(t *testing.T) {
	cfg := &config.Config{Projects: []config.Project{
		{Name: "other", Alias: "other.test", Enabled: true, Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"app.x.io", "ws.x.io", "typed.x.io"}}}},
		{Name: "off", Alias: "off.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"files.x.io"}}}},
	}}
	p := config.Project{Name: "mine", Alias: "mine.test", Routes: []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"App.x.io", "typed.x.io"}},
		{Port: 6001, Role: config.RoleWeb, Hosts: []string{"ws.x.io"}},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"files.x.io"}},
	}}
	inferred := map[string]bool{"app.x.io": true, "ws.x.io": true, "files.x.io": true}
	routes, notes := withoutTakenHosts(cfg, p, inferred)
	want := []config.Route{
		{Port: 80, Role: config.RoleWeb, Hosts: []string{"typed.x.io"}},
		{Port: 6001, Role: config.RoleIgnore},
		{Port: 9000, Role: config.RoleWeb, Hosts: []string{"files.x.io"}},
	}
	if !reflect.DeepEqual(routes, want) {
		t.Errorf("routes = %+v\nwant %+v", routes, want)
	}
	wantNotes := []string{
		`Not serving App.x.io: project "other" already serves it.`,
		`Not serving ws.x.io: project "other" already serves it.`,
	}
	if !reflect.DeepEqual(notes, wantNotes) {
		t.Errorf("notes = %q", notes)
	}
}

// A .nearhostrc that only sets the main name leaves the routes to discovery,
// so an inferred host is still served; the name and the host are both asked
// about.
func TestAddNearhostrcWithOnlyAnAlias(t *testing.T) {
	dir := envProject(t, hostedEnv)
	if err := os.WriteFile(filepath.Join(dir, ".nearhostrc"), []byte("version: 1\nalias: local.myapp.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{})
	enableHostedPreset(t, cfgPath)
	out, err := executeWithInput(t, "y\n", newAddCmd(rcBackend(t, true)), "--config", cfgPath, "add", dir, "--name", "aliasapp")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, "This project asks for these, which need your approval:", "local.myapp.com", "app.local.x.io on port 8220")
	p, _ := loadSaved(t, cfgPath).Project("aliasapp")
	if p.Alias != "local.myapp.com" {
		t.Errorf("alias = %q", p.Alias)
	}
	if hosts := p.AllHosts(); !reflect.DeepEqual(hosts, []string{"local.myapp.com", "app.local.x.io"}) {
		t.Errorf("serves %v", hosts)
	}
}

func TestAddExplainsABadWebPortNameOrPath(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	dir := fixture(t, "sail-custom")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{dir, "--name", "w", "--web", "abc"}, `--web must be a port number, not "abc"`},
		{[]string{dir, "--name", "w", "--web", "70000"}, "--web must be a port between 1 and 65535, not 70000"},
		{[]string{"/nonexistent"}, "no directory at /nonexistent"},
		{[]string{dir, "--name", "ünï"}, `the name "ünï" has no letters a domain can use; pass --name with a-z, 0-9 and -, or --alias for the full domain`},
		{[]string{dir, "--name", "café"}, `the name "café" has no letters a domain can use`},
	} {
		out, err := execute(t, newAddCmd(noRemote(t)), append([]string{"--config", cfgPath, "add"}, c.args...)...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("add %v: err = %v, want %q\n%s", c.args, err, c.want, out)
		}
	}
	// An ASCII name that needs tidying, and a non-ASCII name with its own alias, still work.
	for _, args := range [][]string{{dir, "--name", "My App", "--web", "8080"}, {dir, "--name", "ünï", "--alias", "uni.test", "--web", "8080"}} {
		if out, err := execute(t, newAddCmd(noRemote(t)), append([]string{"--config", cfgPath, "add"}, args...)...); err != nil {
			t.Errorf("add %v: %v\n%s", args, err, out)
		}
	}
}
