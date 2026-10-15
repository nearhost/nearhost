package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

func blogProject(alias string) config.Project {
	return config.Project{Name: "blog", Server: config.LocalServer, Path: "/code/blog", Alias: alias, Enabled: true,
		Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}}
}

func TestEditConfigSavesOnlyWhatPasses(t *testing.T) {
	u := formEngine(t)
	add := func(cfg *config.Config) error {
		cfg.Projects = append(cfg.Projects, blogProject("blog.test"))
		return nil
	}
	if err := u.EditConfig(add); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadSaved(t, u.path).Project("blog"); !ok {
		t.Fatal("the edit was not saved")
	}
	before, _ := os.ReadFile(u.path)
	if err := u.EditConfig(func(*config.Config) error { return errors.New("no") }); err == nil || err.Error() != "no" {
		t.Errorf("a failing edit = %v", err)
	}
	if err := u.EditConfig(add); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("an invalid edit = %v", err)
	}
	if after, _ := os.ReadFile(u.path); string(after) != string(before) {
		t.Errorf("a refused edit changed the file:\n%s", after)
	}

	// The file is read afresh, so a change made by hand is kept.
	cfg := loadSaved(t, u.path)
	cfg.Servers = append(cfg.Servers, config.Server{Name: "mini", SSHHost: "mini"})
	saveConfig(t, u.path, cfg)
	if err := u.EditConfig(func(cfg *config.Config) error { cfg.Projects[0].Alias = "site.test"; return nil }); err != nil {
		t.Fatal(err)
	}
	saved := loadSaved(t, u.path)
	if _, ok := saved.Server("mini"); !ok || saved.Projects[0].Alias != "site.test" {
		t.Errorf("saved %+v", saved)
	}
}

func TestAdviceForAConfiguredProject(t *testing.T) {
	u := formEngine(t)
	ctx := context.Background()
	if notes := u.EnablePresets(ctx, []string{"test/stack"}); len(notes) != 0 {
		t.Fatal(notes)
	}
	files, err := u.OpenServer(ctx, config.Server{Name: config.LocalServer})
	if err != nil {
		t.Fatal(err)
	}
	d, err := files.Discover(ctx, fixture(t, "sail-custom"))
	files.Close()
	if err != nil {
		t.Fatal(err)
	}
	p := config.Project{Name: "shop", Server: config.LocalServer, Path: d.Path, Alias: "shop.test", Enabled: true, Routes: d.Routes}
	if err := u.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	advice, err := u.Advice(ctx, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(advice.Presets, ",") != "test/stack" {
		t.Errorf("presets %v, want test/stack", advice.Presets)
	}
	appURL := false
	for _, a := range advice.Items {
		appURL = appURL || (a.Code == "app-url" && strings.Contains(a.Message, "https://shop.test"))
		if a.Code == "loopback-clash" {
			t.Errorf("the project clashes with itself: %+v", a)
		}
	}
	if !appURL {
		t.Errorf("no app-url advice for shop.test: %+v", advice)
	}
	if _, err := u.Advice(ctx, "gone"); err == nil || err.Error() != "gone is no longer in the config" {
		t.Errorf("Advice(gone) = %v", err)
	}
}

func TestTrustRunsWithTheTerminal(t *testing.T) {
	trust := newFake()
	var shown bytes.Buffer
	u := formEngine(t)
	u.b.up.trust = trust.backend()
	u.suspend = func(_ context.Context, fn func(io.Reader, io.Writer) error) error {
		return fn(strings.NewReader(""), &shown)
	}
	if err := u.Trust(context.Background()); err != nil {
		t.Fatal(err)
	}
	if trust.installs != 1 || !strings.Contains(shown.String(), "You may be asked for your password.") {
		t.Errorf("installs %d, shown %q", trust.installs, shown.String())
	}
	trust.instErr = errors.New("the password was refused")
	if err := u.Trust(context.Background()); err == nil || !strings.Contains(err.Error(), "the password was refused") {
		t.Errorf("a refused trust = %v", err)
	}
}

// Edits made at the same time are each saved: none overwrites another.
func TestEditConfigSerialisesEdits(t *testing.T) {
	u := formEngine(t)
	const n = 20
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			done <- u.EditConfig(func(cfg *config.Config) error {
				p := blogProject("")
				p.Name = "p" + string(rune('a'+i))
				p.Alias = p.Name + ".test"
				p.Routes = []config.Route{{Port: 3000 + i, Role: config.RoleWeb}}
				cfg.Projects = append(cfg.Projects, p)
				return nil
			})
		}()
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := len(loadSaved(t, u.path).Projects); got != n {
		t.Errorf("%d of %d edits saved", got, n)
	}
}
