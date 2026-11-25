package config

import (
	"errors"
	"testing"

	"github.com/nearhost/nearhost/internal/report"
)

// A key Nearhost does not know is a message that says saving would delete
// it and what to do, while the error's text stays as it was.
func TestUnknownKeysMessage(t *testing.T) {
	err := &UnknownKeysError{Path: "/c/config.yaml", Keys: []Unknown{{Key: "host", Line: 12}}}
	m, ok := report.As(err)
	if !ok {
		t.Fatal("no message")
	}
	if m.Code != report.ConfigUnknownKeys {
		t.Errorf("code = %q", m.Code)
	}
	if want := `/c/config.yaml line 12: Nearhost does not know the key "host".`; m.Title != want {
		t.Errorf("title = %q, want %q", m.Title, want)
	}
	if m.Why != "Saving now would delete it." {
		t.Errorf("why = %q", m.Why)
	}
	if len(m.Fix) != 1 || m.Fix[0].Text != "Fix or remove it." {
		t.Errorf("fix = %+v", m.Fix)
	}
	if want := `/c/config.yaml line 12: Nearhost does not know the key "host". Fix or remove it; saving now would delete it.`; err.Error() != want {
		t.Errorf("Error() = %q, want %q", err.Error(), want)
	}

	two := &UnknownKeysError{Path: "/c/config.yaml", Keys: []Unknown{{Key: "host", Line: 12}, {Key: "future", Line: 18}}}
	m, _ = report.As(two)
	if m == nil || m.Why != "Saving now would delete them." || len(m.Fix) != 1 || m.Fix[0].Text != "Fix or remove them." {
		t.Errorf("two keys: message = %+v", m)
	}
}

// A config that breaks a rule is config.invalid, naming the field, and
// still matches what it wraps.
func TestValidateIsConfigInvalid(t *testing.T) {
	c := &Config{Projects: []Project{{Name: "web", Server: LocalServer, Alias: "nearhost.localhost",
		Routes: []Route{{Port: 3000, Role: RoleWeb}}}}}
	err := c.Validate()
	m, ok := report.As(err)
	if !ok || m.Code != report.ConfigInvalid {
		t.Fatalf("Validate = %v, want %q", err, report.ConfigInvalid)
	}
	if m.Data["field"] != "projects.alias" {
		t.Errorf(`Data["field"] = %v`, m.Data["field"])
	}
	if m.Title != err.Error() {
		t.Errorf("title %q differs from the text %q", m.Title, err.Error())
	}
	if !errors.Is(err, errPageHost) {
		t.Error("the error no longer wraps errPageHost")
	}

	c = &Config{Servers: []Server{{Name: "mini"}}}
	if m, _ := report.As(c.Validate()); m == nil || m.Data["field"] != "servers.ssh_host" {
		t.Errorf("no ssh_host: message = %+v", m)
	}
}
