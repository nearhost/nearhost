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

const askServer = "Server apollo is not in the config. Add it? [Y/n]: "

// serverAsking is an add backend for a server the config does not have. The
// config says not to ask about registries, so the server questions are the
// only ones. opened records the server the project was read from.
func serverAsking(t *testing.T, terminal bool, opened *config.Server) (string, []byte, addBackend) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	off := false
	saveConfig(t, cfgPath, &config.Config{RegistryPrompt: &off})
	before, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	b := addBackend{
		openRemote: func(_ context.Context, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
			*opened = srv
			return discovery.DirReader{Root: fixture(t, "compose-literal")}, projectPath, func() error { return nil }, nil
		},
		interactive: func() bool { return terminal },
	}
	return cfgPath, before, b
}

func TestAddOffersToAddAnUnknownServer(t *testing.T) {
	var opened config.Server
	cfgPath, _, b := serverAsking(t, true, &opened)
	out, err := executeWithInput(t, "\n\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	wantContains(t, out, askServer+"SSH host for apollo [apollo]: ",
		"Added server apollo, reached with `ssh apollo`.\nAdded blog on apollo at https://blog.test\n")
	want := config.Server{Name: "apollo", SSHHost: "apollo"}
	if opened != want {
		t.Errorf("read the project from %+v, want %+v", opened, want)
	}
	saved := loadSaved(t, cfgPath)
	if srv, ok := saved.Server("apollo"); !ok || srv != want {
		t.Errorf("saved server = %+v %v", srv, ok)
	}
	if p, ok := saved.Project("blog"); !ok || p.Server != "apollo" {
		t.Errorf("saved project = %+v %v", p, ok)
	}
}

func TestAddTakesTheSSHHostTypedForANewServer(t *testing.T) {
	var opened config.Server
	cfgPath, _, b := serverAsking(t, true, &opened)
	out, err := executeWithInput(t, "y\n -oProxyCommand=x \nhas space\n dev@apollo.lan \n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if n := strings.Count(out, "SSH host for apollo [apollo]: "); n != 3 {
		t.Errorf("asked for the host %d times, want 3:\n%s", n, out)
	}
	wantContains(t, out, "An SSH host cannot start with - or contain spaces.\n", "Added server apollo, reached with `ssh dev@apollo.lan`.\n")
	if srv, _ := loadSaved(t, cfgPath).Server("apollo"); srv.SSHHost != "dev@apollo.lan" || opened.SSHHost != "dev@apollo.lan" {
		t.Errorf("saved %+v, opened %+v", srv, opened)
	}
}

// No, or no answer at all, ends add with the error it gives without a
// terminal, and nothing is read or saved.
func TestAddWithoutAgreementToAddTheServerSavesNothing(t *testing.T) {
	for name, input := range map[string]string{"no": "n\n", "end of input": "", "end of input at the host": "y\n"} {
		t.Run(name, func(t *testing.T) {
			var opened config.Server
			cfgPath, before, b := serverAsking(t, true, &opened)
			out, err := executeWithInput(t, input, newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo", "--web", "9000")
			if err == nil || !strings.Contains(err.Error(), `unknown server "apollo"`) {
				t.Fatalf("expected the unknown server error, got %v\n%s", err, out)
			}
			wantContains(t, out, askServer)
			if opened != (config.Server{}) {
				t.Errorf("read the project from %+v", opened)
			}
			if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
				t.Errorf("config changed:\n%s", after)
			}
		})
	}
}

func TestAddWithoutATerminalDoesNotOfferToAddAServer(t *testing.T) {
	var opened config.Server
	cfgPath, before, b := serverAsking(t, false, &opened)
	out, err := executeWithInput(t, "y\n\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo", "--web", "9000")
	if err == nil || !strings.Contains(err.Error(), `unknown server "apollo"`) {
		t.Fatalf("expected the unknown server error, got %v\n%s", err, out)
	}
	if strings.Contains(out, "Add it?") {
		t.Errorf("asked without a terminal:\n%s", out)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Errorf("config changed:\n%s", after)
	}
}

// A server agreed to is saved with the project and not before: a failure on
// the way, or a save made for another reason, leaves it out.
func TestAddSavesANewServerOnlyWithTheProject(t *testing.T) {
	t.Run("unreachable", func(t *testing.T) {
		var opened config.Server
		cfgPath, before, b := serverAsking(t, true, &opened)
		b.openRemote = func(context.Context, config.Server, string) (discovery.FileReader, string, func() error, error) {
			return nil, "", nil, errors.New("ssh apollo failed: Permission denied (publickey).")
		}
		_, err := executeWithInput(t, "\n\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo")
		if err == nil || !strings.Contains(err.Error(), "Permission denied") {
			t.Fatalf("expected the ssh error, got %v", err)
		}
		if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
			t.Errorf("config changed:\n%s", after)
		}
	})
	t.Run("no web route", func(t *testing.T) {
		var opened config.Server
		cfgPath, _, b := serverAsking(t, true, &opened)
		// Without --web, the answer to the web question is "none".
		_, err := executeWithInput(t, "\n\n0\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo")
		if err == nil || !strings.Contains(err.Error(), "nothing was saved") {
			t.Fatalf("expected no web route, got %v", err)
		}
		if _, ok := loadSaved(t, cfgPath).Server("apollo"); ok {
			t.Error("the server was saved without its project")
		}
	})
}

// A server another add saved while this one asked and read is not saved
// twice, which the config would refuse.
func TestAddSavesANewServerOnceWhenAnotherAddSavedIt(t *testing.T) {
	var opened config.Server
	cfgPath, _, b := serverAsking(t, true, &opened)
	open := b.openRemote
	b.openRemote = func(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
		if err := config.Update(cfgPath, func(cfg *config.Config) error {
			cfg.Servers = append(cfg.Servers, srv)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return open(ctx, srv, projectPath)
	}
	out, err := executeWithInput(t, "\n\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo", "--web", "9000")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if strings.Contains(out, "Added server") {
		t.Errorf("said it added the server another add saved:\n%s", out)
	}
	saved := loadSaved(t, cfgPath)
	if len(saved.Servers) != 1 {
		t.Errorf("saved servers = %+v, want apollo once", saved.Servers)
	}
	if p, ok := saved.Project("blog"); !ok || p.Server != "apollo" {
		t.Errorf("saved project = %+v %v", p, ok)
	}
}

// A server another add saved meanwhile with another SSH host is not the one
// the project was read from, so nothing is saved.
func TestAddRefusesWhenAnotherAddSavedTheServerElsewhere(t *testing.T) {
	var opened config.Server
	cfgPath, _, b := serverAsking(t, true, &opened)
	open := b.openRemote
	b.openRemote = func(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, string, func() error, error) {
		if err := config.Update(cfgPath, func(cfg *config.Config) error {
			cfg.Servers = append(cfg.Servers, config.Server{Name: srv.Name, SSHHost: "other.lan"})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return open(ctx, srv, projectPath)
	}
	out, err := executeWithInput(t, "\n\n", newAddCmd(b), "--config", cfgPath, "add", "/srv/www/blog", "--server", "apollo", "--web", "9000")
	if err == nil || !strings.Contains(err.Error(), "ssh other.lan") {
		t.Fatalf("add = %v, want the other host named\n%s", err, out)
	}
	saved := loadSaved(t, cfgPath)
	if _, ok := saved.Project("blog"); ok {
		t.Error("the project was saved under the other server")
	}
}
