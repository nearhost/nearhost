package engine

import (
	"reflect"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
)

// A copy of the configuration carries every top-level field, so what the
// core reports to its clients (keep_running, notifications, the registries
// and presets) is what its configuration holds. The test walks the fields of
// config.Config, so a field added later fails it until the copy handles it.
func TestCloneConfigCopiesEveryField(t *testing.T) {
	yes, no := true, false
	keep := true
	cfg := &config.Config{
		Servers:        []config.Server{{Name: "box", SSHHost: "box"}},
		Projects:       []config.Project{{Name: "p", Server: config.LocalServer, Alias: "p.test", Routes: []config.Route{{Port: 80, Role: config.RoleWeb, Hosts: []string{"p.test"}}}}},
		Registries:     []config.Registry{{Alias: "official", Repo: "nearhost/presets"}},
		RegistryPrompt: &no,
		Presets:        []config.PresetRef{{Name: "official/laravel", Ref: "abc", SHA256: "def"}},
		Notifications:  &yes,
		KeepRunning:    &keep,
		Network: &config.Network{Enabled: &yes, Domain: "lab.example.dev", Certificates: "acme",
			ACME: &config.NetworkACME{DNS: "cloudflare"}, Allow: []string{"10.0.0.0/8"}},
	}
	orig := reflect.ValueOf(cfg).Elem()
	for i := 0; i < orig.NumField(); i++ {
		if orig.Field(i).IsZero() {
			t.Fatalf("the test's configuration leaves %s unset; set it so the copy of it is checked", orig.Type().Field(i).Name)
		}
	}

	out := cloneConfig(cfg)
	if !reflect.DeepEqual(out, cfg) {
		t.Fatalf("cloneConfig = %+v, want %+v", out, cfg)
	}
	copied := reflect.ValueOf(out).Elem()
	for i := 0; i < orig.NumField(); i++ {
		name := orig.Type().Field(i).Name
		a, b := orig.Field(i), copied.Field(i)
		switch a.Kind() {
		case reflect.Pointer:
			if a.Pointer() == b.Pointer() {
				t.Errorf("%s shares its pointer with the original", name)
			}
		case reflect.Slice:
			if a.Pointer() == b.Pointer() {
				t.Errorf("%s shares its backing array with the original", name)
			}
		}
	}

	// Changing the original leaves the copy as it was.
	*cfg.KeepRunning = false
	*cfg.Notifications = false
	*cfg.RegistryPrompt = true
	cfg.Registries[0].Alias = "changed"
	cfg.Presets[0].Name = "changed"
	cfg.Servers[0].Name = "changed"
	cfg.Projects[0].Routes[0].Hosts[0] = "changed"
	*cfg.Network.Enabled = false
	cfg.Network.ACME.DNS = "changed"
	cfg.Network.Allow[0] = "changed"
	if !*out.KeepRunning || !*out.Notifications || *out.RegistryPrompt ||
		out.Registries[0].Alias != "official" || out.Presets[0].Name != "official/laravel" ||
		out.Servers[0].Name != "box" || out.Projects[0].Routes[0].Hosts[0] != "p.test" ||
		!*out.Network.Enabled || out.Network.ACME.DNS != "cloudflare" || out.Network.Allow[0] != "10.0.0.0/8" {
		t.Fatalf("the copy changed with the original: %+v", out)
	}
}
