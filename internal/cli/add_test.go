package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
)

// noRemote is an add backend for tests that must stay on this machine.
func noRemote(t *testing.T) addBackend {
	return addBackend{openRemote: func(context.Context, config.Server, string) (discovery.FileReader, func() error, error) {
		t.Error("a local add opened a remote session")
		return nil, nil, errors.New("unexpected")
	}}
}

func TestAddDiscoversLocalProjectAndSavesConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	dir := fixture(t, "sail-custom")
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", dir, "--name", "myapp")
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
	want := "Routes found for myapp (local:" + dir + "):\n" +
		"  PORT  ROLE      LABEL\n" +
		"  1025  ignore    Mailpit SMTP (FORWARD_MAILPIT_PORT)\n" +
		"  3307  ignore    Database (FORWARD_DB_PORT)\n" +
		"  5174  loopback  Vite (VITE_PORT)\n" +
		"  6379  ignore    Redis (FORWARD_REDIS_PORT)\n" +
		"  8025  ignore    Mailpit dashboard (FORWARD_MAILPIT_DASHBOARD_PORT)\n" +
		"  8080  web       Laravel (APP_PORT)\n" +
		"\n" +
		"Advisory (app-url): APP_URL is \"http://localhost\". Generated links will use it, so set it to the alias.\n" +
		"  APP_URL=https://myapp.test\n" +
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
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", "--disabled")
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
	wantContains(t, out, "  8080  ignore    Laravel (APP_PORT)\n", "  9000  web       web (--web)\n")

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
		{"invalid alias", []string{"add", dir, "--name", "other", "--alias", "Other.example", "--web", "9000"}, []string{"Other.example", ".test"}},
		{"loopback clash", []string{"add", fixture(t, "sail-custom"), "--name", "second"}, []string{"loopback port 5174", `"shop"`, "--disabled"}},
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
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "sail-custom"), "--name", "second", "--disabled")
	if err != nil {
		t.Fatalf("add --disabled: %v\n%s", err, out)
	}
	wantContains(t, out, "Advisory (loopback-clash): Port 5174 is also forwarded to this machine by project \"shop\".", "  VITE_PORT=5173\n")
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
	b := addBackend{openRemote: func(_ context.Context, srv config.Server, projectPath string) (discovery.FileReader, func() error, error) {
		gotServer, gotPath = srv, projectPath
		return discovery.DirReader{Root: fixture(t, "compose-literal")}, func() error { closed++; return nil }, nil
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

func TestAddRemoteFailureSavesNothing(t *testing.T) {
	cfgPath, before := existing(t)
	b := addBackend{openRemote: func(context.Context, config.Server, string) (discovery.FileReader, func() error, error) {
		return nil, nil, errors.New("ssh mini.lan failed: Permission denied (publickey).")
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
		out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name="+name, "--alias", "x.test", "--disabled")
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
	out, err := execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "padded", "--alias", " padded.test\t")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if p, _ := loadSaved(t, cfgPath).Project("padded"); p.Alias != "padded.test" {
		t.Errorf("saved the alias %q", p.Alias)
	}
	wantContains(t, out, "at https://padded.test\n")

	// Blanks do not hide a duplicate either.
	out, err = execute(t, newAddCmd(noRemote(t)), "--config", cfgPath, "add", fixture(t, "compose-literal"), "--name", "again", "--alias", "shop.test ")
	if err == nil || !strings.Contains(err.Error(), `already used by project "shop"`) {
		t.Errorf("a padded duplicate alias: %v\n%s", err, out)
	}
}
