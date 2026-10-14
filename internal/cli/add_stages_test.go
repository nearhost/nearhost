package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/testutil"
)

func TestNameAndAliasProblems(t *testing.T) {
	// Hostnames ignore case, so an alias typed in capitals by hand still
	// takes the lower-case one.
	cfg := &config.Config{Projects: []config.Project{{Name: "blog", Alias: "BLOG.test"}}}
	for _, name := range []string{"", ".", "~", "a/b", `a\b`} {
		if err := nameProblem(cfg, name); !errors.Is(err, errBadName) {
			t.Errorf("nameProblem(%q) = %v", name, err)
		}
	}
	var taken *nameTakenError
	if err := nameProblem(cfg, "blog"); !errors.As(err, &taken) || taken.other.Name != "blog" {
		t.Errorf("nameProblem(blog) = %v", err)
	}
	if err := nameProblem(cfg, "shop"); err != nil {
		t.Errorf("nameProblem(shop) = %v", err)
	}
	var aliasTaken *aliasTakenError
	if err := aliasProblem(cfg, "blog.test"); !errors.As(err, &aliasTaken) || aliasTaken.other.Name != "blog" {
		t.Errorf("aliasProblem(blog.test) = %v", err)
	}
	if err := aliasProblem(cfg, "bad alias"); err == nil {
		t.Error("an alias with a space was accepted")
	}
	if err := aliasProblem(cfg, "shop.test"); err != nil {
		t.Errorf("aliasProblem(shop.test) = %v", err)
	}
}

func TestLocalFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, d := range []string{"code/blog", "code/shop", "code/.hidden"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "code", "notes.txt"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "code", "blog"), filepath.Join(home, "code", "linked")); err != nil {
		t.Fatal(err)
	}
	var f serverFiles = localFiles{}
	if got, err := f.resolve("~/code/blog"); err != nil || got != filepath.Join(home, "code", "blog") {
		t.Errorf("resolve(~/code/blog) = %q, %v", got, err)
	}
	if _, err := f.resolve("~/code/notes.txt"); err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Errorf("resolve of a file = %v", err)
	}
	if _, err := f.resolve("~/code/missing"); err == nil {
		t.Error("resolve of a missing directory succeeded")
	}
	got, err := f.listDirs(filepath.Join(home, "code"))
	if want := []string{".hidden", "blog", "linked", "shop"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("listDirs = %v, %v; want %v", got, err, want)
	}
}

// sftpOnly is an ssh client whose servers allow SFTP only, served from home.
func sftpOnly(t *testing.T, home string) sshClient {
	t.Helper()
	server := sftpServer(t)
	wrapper := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=\"$a\"; done\n" +
		"if [ \"$last\" = sftp ]; then exec '" + server + "' -R -d '" + home + "'; fi\n" +
		"exec '" + testutil.FakeSSH(t) + "' \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return sshClient{path: wrapper, env: []string{"FAKE_SSH_LOG=" + filepath.Join(t.TempDir(), "ssh.log")}}
}

func TestServerFilesOverSFTP(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"code/blog", "code/shop"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files, err := openFiles(context.Background(), sftpOnly(t, home), config.Server{Name: "mini", SSHHost: "mini.lan"}, "add the project again")
	if err != nil {
		t.Fatal(err)
	}
	defer files.close()
	real, _ := filepath.EvalSymlinks(home) // sftp-server reports the real path
	if got, err := files.resolve("~/code/blog"); err != nil || got != filepath.Join(real, "code", "blog") {
		t.Errorf("resolve = %q, %v", got, err)
	}
	if _, err := files.resolve("~/code/missing"); err == nil || !strings.Contains(err.Error(), "on server mini") {
		t.Errorf("resolve of a missing directory = %v", err)
	}
	if got, err := files.listDirs(filepath.Join(real, "code")); err != nil || !reflect.DeepEqual(got, []string{"blog", "shop"}) {
		t.Errorf("listDirs = %v, %v", got, err)
	}
}

func TestDiscoverOnceOffersTheTrustedRegistriesPresets(t *testing.T) {
	cfgPath, _, b := trusted(t)
	cfg := loadSaved(t, cfgPath)
	sources := b.indexes(context.Background(), cfg, cfgPath)
	reader := localFiles{}.reader(fixture(t, "sail-custom"))
	res, _, err := discoverOnce(cfg, cfgPath, reader, sources, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range res.Suggested {
		names = append(names, o.Name)
	}
	if !reflect.DeepEqual(names, []string{"test/stack", "test/bundler"}) && !reflect.DeepEqual(names, []string{"test/bundler", "test/stack"}) {
		t.Errorf("suggested %v", names)
	}
	res, _, err = discoverOnce(cfg, cfgPath, reader, sources, map[string]bool{"test/stack": true, "test/bundler": true})
	if err != nil || len(res.Suggested) != 0 {
		t.Errorf("passed presets offered again: %v, %v", res.Suggested, err)
	}
}

func TestSaveProjectRefusesALoopbackClash(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	web := config.Route{Port: 80, Role: config.RoleWeb}
	lb := config.Route{Port: 5173, Role: config.RoleLoopback}
	cfg := &config.Config{Servers: []config.Server{{Name: "mini", SSHHost: "mini"}}, Projects: []config.Project{
		{Name: "a", Server: "mini", Path: "/a", Alias: "a.test", Enabled: true, Routes: []config.Route{web, lb}},
	}}
	saveConfig(t, cfgPath, cfg)
	b := config.Project{Name: "b", Server: "mini", Path: "/b", Alias: "b.test", Enabled: true, Routes: []config.Route{web, lb}}
	if err := saveProject(loadSaved(t, cfgPath), cfgPath, b); err == nil || !strings.Contains(err.Error(), "loopback port 5173") {
		t.Fatalf("saveProject = %v", err)
	}
	if _, ok := loadSaved(t, cfgPath).Project("b"); ok {
		t.Error("the clashing project was saved")
	}
	b.Routes = []config.Route{web}
	if err := saveProject(loadSaved(t, cfgPath), cfgPath, b); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSaved(t, cfgPath).Project("b"); !ok {
		t.Error("the project was not saved")
	}
}

func TestAddServer(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	saveConfig(t, cfgPath, &config.Config{Servers: []config.Server{{Name: "mini", SSHHost: "mini"}}})
	for _, srv := range []config.Server{
		{Name: "local", SSHHost: "x"},
		{Name: "mini", SSHHost: "x"},
		{Name: "", SSHHost: "x"},
		{Name: "apollo", SSHHost: "-oProxyCommand=x"},
		{Name: "apollo", SSHHost: "has space"},
	} {
		if err := addServer(loadSaved(t, cfgPath), cfgPath, srv); err == nil {
			t.Errorf("addServer(%+v) succeeded", srv)
		}
	}
	if err := addServer(loadSaved(t, cfgPath), cfgPath, config.Server{Name: "apollo", SSHHost: "dev@apollo.lan"}); err != nil {
		t.Fatal(err)
	}
	if srv, ok := loadSaved(t, cfgPath).Server("apollo"); !ok || srv.SSHHost != "dev@apollo.lan" {
		t.Errorf("saved %+v %v", srv, ok)
	}
}
