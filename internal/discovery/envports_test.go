package discovery

import (
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
)

func TestPortVarLabels(t *testing.T) {
	cases := map[string]string{
		"APP_PORT":        "app",
		"PORT_VITE":       "vite",
		"FORWARD_DB_PORT": "forward-db",
		"DB_PORT_TEST":    "db-test",
		"PORT":            "port",
		"port":            "port",
		"App_Port":        "app",
		"WEB_PORTS":       "web",
		"API-PORT":        "api",
		"A__PORT":         "a",
		"PORT_PORTS":      "port",
	}
	for name, want := range cases {
		got, ok := portVarLabel(name)
		if !ok || got != want {
			t.Errorf("portVarLabel(%q) = %q, %v; want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"REPORT_LIMIT", "IMPORT_BATCH", "SUPPORT", "PORTAL", "APP_URL", ""} {
		if got, ok := portVarLabel(name); ok {
			t.Errorf("portVarLabel(%q) = %q; want no match", name, got)
		}
	}
}

func TestEnvPortValues(t *testing.T) {
	env := map[string]string{
		"A_PORT": "0",
		"B_PORT": "65536",
		"C_PORT": "80a",
		"D_PORT": " 8080 ",
		"E_PORT": "${OTHER}",
		"F_PORT": "-80",
		"G_PORT": "+81",
		"H_PORT": "",
		"I_PORT": "65535",
		"J_PORT": "1",
	}
	got := map[int]string{}
	for _, c := range envPorts(env) {
		got[c.Port] = c.Label
	}
	want := map[int]string{8080: "d (D_PORT)", 65535: "i (I_PORT)", 1: "j (J_PORT)"}
	if len(got) != len(want) {
		t.Fatalf("envPorts = %v, want %v", got, want)
	}
	for port, label := range want {
		if got[port] != label {
			t.Errorf("port %d = %q, want %q", port, got[port], label)
		}
	}
}

func TestEnvPortsMergesVariablesOnOnePort(t *testing.T) {
	env := map[string]string{"FORWARD_APP_PORT": "8080", "APP_PORT": "8080", "OTHER_PORT": "8080", "DB_PORT": "3306"}
	got := envPorts(env)
	if len(got) != 2 {
		t.Fatalf("envPorts = %+v", got)
	}
	db, app := got[0], got[1]
	if db.Port != 3306 || db.Label != "db (DB_PORT)" || db.Source != "env:DB_PORT" || db.Var != "DB_PORT" || db.Role != config.RoleIgnore {
		t.Errorf("db = %+v", db)
	}
	if app.Port != 8080 || app.Label != "app (APP_PORT, FORWARD_APP_PORT, OTHER_PORT)" || app.Source != "env:APP_PORT" || app.Var != "APP_PORT" || app.Role != config.RoleIgnore {
		t.Errorf("app = %+v", app)
	}
}

func TestDiscoverProposesEnvPortsWithoutPresets(t *testing.T) {
	res := discover(t, memReader{".env": "APP_PORT=8080\nFORWARD_DB_PORT=3306\nREPORT_LIMIT=50\n", ".env.local": "APP_PORT=8081\n"}, Options{})
	m := routeMap(res)
	if len(res.Routes) != 2 || m[8081].Label != "app (APP_PORT)" || m[3306].Label != "forward-db (FORWARD_DB_PORT)" {
		t.Fatalf("routes = %+v", res.Routes)
	}
	if m[8081].Role != config.RoleIgnore || m[3306].Role != config.RoleIgnore || m[8081].Source != "env:APP_PORT" || m[8081].Preset != "" {
		t.Errorf("with two ports nothing says which is web: %+v", res.Routes)
	}
}

func TestDiscoverLoneEnvPortIsWeb(t *testing.T) {
	res := discover(t, memReader{".env": "APP_PORT=8080\n"}, Options{})
	if len(res.Routes) != 1 || res.Routes[0].Role != config.RoleWeb || res.Routes[0].Label != "app (APP_PORT)" {
		t.Fatalf("routes = %+v", res.Routes)
	}
}

// Presets claim ports before the core reads .env, and a port a preset or
// Compose already has is not proposed again.
func TestDiscoverEnvPortsComeAfterPresets(t *testing.T) {
	p := inline(t, "x", "detect: 'true'\nports:\n  - {var: APP_PORT, label: App, web: preferred}\nroutes:\n  - {id: dev, port: '5173', label: Dev}\n")
	res := discover(t, memReader{".env": "APP_PORT=8080\nFORWARD_APP_PORT=8080\nVITE_PORT=5173\nDB_PORT=3306\n"}, Options{Presets: []*preset.Preset{p}})
	m := routeMap(res)
	if len(res.Routes) != 3 {
		t.Fatalf("routes = %+v", res.Routes)
	}
	if m[8080].Label != "App (APP_PORT)" || m[8080].Role != config.RoleWeb || m[8080].Preset != "x" {
		t.Errorf("the preset relabels its variable: %+v", m[8080])
	}
	if m[5173].Label != "Dev" || m[5173].Source != "preset:x" {
		t.Errorf("a preset route claims its port first: %+v", m[5173])
	}
	if m[3306].Label != "db (DB_PORT)" || m[3306].Source != "env:DB_PORT" {
		t.Errorf("an unclaimed port comes from .env: %+v", m[3306])
	}
}
