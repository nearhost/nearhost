package discovery

import (
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
)

func find(advs []Advisory, code string) Advisory {
	for _, a := range advs {
		if a.Code == code {
			return a
		}
	}
	return Advisory{}
}

// shop is an enabled project that forwards the given loopback ports.
func shop(enabled bool, ports ...int) []config.Project {
	p := config.Project{Name: "shop", Enabled: enabled}
	for _, port := range ports {
		p.Routes = append(p.Routes, config.Route{Port: port, Role: config.RoleLoopback})
	}
	return []config.Project{p}
}

func TestAdviseIsSilentWithoutPresets(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{})
	advs, warnings := Advise(res, "myapp.test", nil, shop(true, 5174))
	if len(advs) != 0 || len(warnings) != 0 {
		t.Fatalf("expected nothing, got %+v %v", advs, warnings)
	}
}

func TestAdviseAsksActivePresets(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{Presets: testPresets(t, "stack", "bundler")})
	advs, warnings := Advise(res, "myapp.test", nil, nil)
	if codes(advs) != "app-url" || len(warnings) != 0 {
		t.Fatalf("expected only app-url, got %s %v", codes(advs), warnings)
	}
	if a := advs[0]; a.Message != "APP_URL should be https://myapp.test." || a.Snippet != "APP_URL=https://myapp.test" {
		t.Errorf("advisory = %+v", a)
	}
}

// stack replaces bundler's default-port advisory and suppresses its
// allowed-hosts advisory. Its own loopback port 5173 is taken, so the free
// port is the next one.
func TestAdviseLaterPresetReplacesAndSuppresses(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-defaults"}, Options{Presets: testPresets(t, "stack", "bundler")})
	advs, warnings := Advise(res, "myapp.test", nil, nil)
	if codes(advs) != "app-url,default-port" || len(warnings) != 0 {
		t.Fatalf("codes = %s, warnings = %v", codes(advs), warnings)
	}
	if a := find(advs, "default-port"); a.Message != "Set VITE_PORT in .env." || a.Snippet != "VITE_PORT=5174" {
		t.Errorf("default-port = %+v", a)
	}
}

func TestAdvisePresetOnItsOwn(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/vite-plain"}, Options{Presets: testPresets(t, "stack", "bundler")})
	advs, _ := Advise(res, "myapp.test", nil, nil)
	if codes(advs) != "allowed-hosts" || advs[0].Message != "Add myapp.test to allowedHosts." || advs[0].Snippet != "" {
		t.Fatalf("advisories = %+v", advs)
	}
	if advs, _ := Advise(res, "foo.test", nil, nil); len(advs) != 0 {
		t.Fatalf("foo.test is allowed, got %+v", advs)
	}
}

func TestAdviseLoopbackClash(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{Presets: testPresets(t, "stack", "bundler")})
	advs, _ := Advise(res, "myapp.test", nil, shop(true, 5174, 5175))
	if codes(advs) != "loopback-clash,app-url" {
		t.Fatalf("codes = %s", codes(advs))
	}
	clash := advs[0]
	if !strings.Contains(clash.Message, `Port 5174 is also forwarded to this machine by project "shop"`) {
		t.Errorf("message = %q", clash.Message)
	}
	if clash.Snippet != "VITE_PORT=5176" {
		t.Errorf("snippet = %q, want the variable that set the port and the next free port", clash.Snippet)
	}
	if advs, _ := Advise(res, "myapp.test", nil, shop(false, 5174)); codes(advs) != "app-url" {
		t.Errorf("a disabled project binds nothing: %s", codes(advs))
	}
}

func TestAdviseLoopbackClashOnALiteralPortHasNoSnippet(t *testing.T) {
	p := inline(t, "x", "detect: 'true'\nroutes:\n  - {id: hot, port: '5174', label: Hot, role: loopback}\n")
	res := discover(t, memReader{}, Options{Presets: []*preset.Preset{p}})
	advs, _ := Advise(res, "x.test", nil, shop(true, 5174))
	if codes(advs) != "loopback-clash" || advs[0].Snippet != "" {
		t.Fatalf("advisories = %+v", advs)
	}
}

// The advisories are about the roles the project ends up with, which the
// caller may have changed since discovery.
func TestAdviseUsesTheRolesAsTheyAreNow(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-custom"}, Options{Presets: testPresets(t, "stack", "bundler")})
	for i := range res.Routes {
		if res.Routes[i].Port == 5174 {
			res.Routes[i].Role = config.RoleWeb
		}
	}
	if advs, _ := Advise(res, "myapp.test", nil, shop(true, 5174)); codes(advs) != "app-url" {
		t.Fatalf("codes = %s", codes(advs))
	}
}

func TestAdviseFreePortSkipsOtherProjects(t *testing.T) {
	res := discover(t, DirReader{Root: "testdata/sail-defaults"}, Options{Presets: testPresets(t, "stack", "bundler")})
	advs, _ := Advise(res, "myapp.test", nil, shop(true, 5174, 5175))
	if a := find(advs, "default-port"); a.Snippet != "VITE_PORT=5176" {
		t.Errorf("snippet = %q", a.Snippet)
	}
}

// A file only an advisory reads may fail to be read. The advisory cannot be
// worked out, and saying nothing would look like it does not apply.
func TestAdviseWarnsWhenAnAdvisoryCannotReadAFile(t *testing.T) {
	p := inline(t, "x", "detect: 'true'\nadvisories:\n  - {id: marker, when: 'file(\"marker\")', message: Found}\n")
	r := failReader{memReader: memReader{"marker": "1"}, fail: "marker"}
	res := discover(t, r, Options{Presets: []*preset.Preset{p}})
	for i := 0; i < 2; i++ {
		advs, warnings := Advise(res, "x.test", nil, nil)
		if len(advs) != 0 {
			t.Errorf("advisories = %+v", advs)
		}
		if len(warnings) != 1 || warnings[0] != "advisories could not read marker: permission denied" {
			t.Errorf("call %d: warnings = %q", i+1, warnings)
		}
	}
}

func TestAdviseWarnsAndCarriesOnWhenAnAdvisoryFails(t *testing.T) {
	p := inline(t, "x", "detect: 'true'\nadvisories:\n  - {id: broken, when: facts.nope, message: Broken}\n  - {id: fine, when: 'true', message: Fine}\n")
	res := discover(t, memReader{}, Options{Presets: []*preset.Preset{p}})
	advs, warnings := Advise(res, "x.test", nil, nil)
	if codes(advs) != "fine" {
		t.Errorf("codes = %s", codes(advs))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "preset x: advisory broken") {
		t.Errorf("warnings = %q", warnings)
	}
}

func TestAdviseGetsHosts(t *testing.T) {
	p := inline2(t, "h", "detect: 'true'\nadvisories:\n  - {id: add-host, when: '!serves(\"app.x.io\")', message: Add it}\n")
	res := discover(t, memReader{}, Options{Presets: []*preset.Preset{p}})
	if advs, _ := Advise(res, "a.test", []string{"a.test"}, nil); codes(advs) != "add-host" {
		t.Errorf("without the host: %s", codes(advs))
	}
	if advs, _ := Advise(res, "a.test", []string{"a.test", "app.x.io"}, nil); codes(advs) != "" {
		t.Errorf("with the host: %s", codes(advs))
	}
}
