package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/nearhost/nearhost/internal/config"
)

// filterConfig has projects on Local and on two servers.
func filterConfig() *config.Config {
	cfg := sampleConfig()
	cfg.Servers = append(cfg.Servers, config.Server{Name: "vps", SSHHost: "vps.example"})
	cfg.Projects = append(cfg.Projects,
		config.Project{Name: "ipbar", Server: config.LocalServer, Path: "/code/ipbar", Alias: "ipbar.test",
			Routes: []config.Route{{Port: 3000, Role: config.RoleWeb}}},
		config.Project{Name: "clientsite", Server: "vps", Path: "/srv/clientsite", Alias: "clientsite.test",
			Routes: []config.Route{{Port: 80, Role: config.RoleWeb}}})
	return cfg
}

// / filters the sidebar by name: projects that match, under their servers,
// with the match marked, and how many match.
func TestFilterByName(t *testing.T) {
	m := sized(newFake(t, filterConfig()))
	press(m, "/")
	for _, k := range []string{"S", "i", "t", "e"} {
		press(m, k)
	}
	got := strings.Join(sideText(m), "\n")
	for _, want := range []string{" / Site", " 2 of 5 match", " ▾ local             1 of 2 ", "▌  ○ website", " ▾ vps", "   ○ clientsite"} {
		if !strings.Contains(got, want) {
			t.Errorf("sidebar lacks %q:\n%s", want, got)
		}
	}
	for _, gone := range []string{"home", "myapp", "ipbar", "mini"} {
		if strings.Contains(got, gone) {
			t.Errorf("sidebar shows %q:\n%s", gone, got)
		}
	}
	if m.sel != rowKey(rowProject, "website") {
		t.Errorf("selected %q, want the first match", m.sel)
	}
	var line string
	side, _ := m.sidebar(m.sideWidth(), m.bodyHeight())
	for _, l := range side {
		if strings.Contains(ansi.Strip(l), "clientsite") {
			line = l
		}
	}
	if !strings.Contains(line, "\x1b[4;") {
		t.Errorf("the match in clientsite is not underlined: %q", line)
	}
}

// Keys move among the matches while typing; enter keeps the filter and esc
// clears it.
func TestFilterKeys(t *testing.T) {
	m := sized(newFake(t, filterConfig()))
	press(m, "/")
	press(m, "s")
	press(m, "down")
	press(m, "down")
	if m.sel != rowKey(rowProject, "clientsite") {
		t.Errorf("down moved to %q", m.sel)
	}
	press(m, "enter")
	if m.filtering || m.query() != "s" {
		t.Errorf("enter: filtering %v, query %q", m.filtering, m.query())
	}
	press(m, "q") // not typed: the filter is kept, and q quits
	if !m.quitting {
		t.Error("q did not quit with the filter kept")
	}
}

func TestEscClearsTheFilter(t *testing.T) {
	m := sized(newFake(t, filterConfig()))
	press(m, "/")
	press(m, "z")
	if got := strings.Join(sideText(m), "\n"); !strings.Contains(got, "0 of 5 match") {
		t.Errorf("no matches:\n%s", got)
	}
	press(m, "enter")
	press(m, "esc")
	if m.query() != "" || !strings.Contains(strings.Join(sideText(m), "\n"), "home") {
		t.Errorf("esc left the filter %q", m.query())
	}
	press(m, "j") // nothing to move among is fine
}

// A name whose lower case is longer than it is matched and marked whole
// runes at a time.
func TestFilterMatchesRunes(t *testing.T) {
	segs := highlight("İzmirİstanbul", "istan", seg{style: styleText})
	if len(segs) != 3 || segs[0].text != "İzmir" || segs[1].text != "İstan" || segs[2].text != "bul" {
		t.Errorf("highlight = %+v", segs)
	}
	if !matches("Café", "FÉ") || matches("Café", "cafés") {
		t.Error("matches is wrong about Café")
	}
}
