package nearhostrc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
)

func TestParseFillsVariablesInPortsAndHosts(t *testing.T) {
	env := map[string]string{"APP_PORT": "8220", "APP_DOMAIN": "local.dd.io"}
	f, err := Parse([]byte("version: 1\nroutes:\n  - port: ${APP_PORT}\n    hosts: [app.${APP_DOMAIN}, \"*.status.${APP_DOMAIN}\"]\n  - port: ${REVERB:-8080}\n    label: reverb\n    hosts: [ws.${APP_DOMAIN}]\n"), env)
	if err != nil {
		t.Fatal(err)
	}
	want := []Route{
		{Port: 8220, Role: config.RoleWeb, Hosts: []string{"app.local.dd.io", "*.status.local.dd.io"}},
		{Port: 8080, Role: config.RoleWeb, Label: "reverb", Hosts: []string{"ws.local.dd.io"}},
	}
	if !reflect.DeepEqual(f.Routes, want) {
		t.Fatalf("Routes = %+v", f.Routes)
	}
	if len(f.SHA256) != 64 {
		t.Fatalf("SHA256 = %q", f.SHA256)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{ // file → substring of the error
		"version: 2\nroutes: []\n":                                                    "update Nearhost",
		"version: 2\nroutes: []\nextra: 1\n":                                          "update Nearhost",
		"version: 1\nroutes: []\nextra: 1\n":                                          "extra",
		"version: 1\nroutes:\n  - port: ${MISSING}\n":                                 "MISSING",
		"version: 1\nroutes:\n  - port: ${P}\n":                                       `"P"`, // env P=abc: not a port
		"version: 1\nroutes:\n  - port: 70000\n":                                      "70000",
		"version: 1\nroutes:\n  - port: 1\n    hosts: [\"*.com\"]\n":                  `"*.com"`,
		"version: 1\nroutes:\n  - port: 1\n    role: loopback\n    hosts: [a.b.io]\n": "only web routes have hosts",
		"routes: []\n": "version",
	}
	for file, want := range cases {
		_, err := Parse([]byte(file), map[string]string{"P": "abc"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Parse(%q) = %v, want an error containing %q", file, err, want)
		}
		if err != nil && !strings.HasPrefix(err.Error(), ".nearhostrc") {
			t.Errorf("Parse(%q) error %q does not start with .nearhostrc", file, err)
		}
	}
}

func TestParseToleratesCRLFAndBOM(t *testing.T) {
	data := []byte("\ufeffversion: 1\r\nroutes:\r\n  - port: 3000\r\n    hosts: [a.local.x.io]\r\n")
	if _, err := Parse(data, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSHA256FollowsTheFilledValues(t *testing.T) {
	file := []byte("version: 1\nroutes:\n  - port: ${P}\n")
	a, _ := Parse(file, map[string]string{"P": "1"})
	b, _ := Parse(file, map[string]string{"P": "2"})
	c, _ := Parse(append([]byte("# a comment\n"), file...), map[string]string{"P": "1"})
	if a.SHA256 == b.SHA256 || a.SHA256 != c.SHA256 {
		t.Fatal("hash must follow filled values and ignore comments and formatting")
	}
}

func TestReadUsesEnvLocalOverEnv(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		FileName:     "version: 1\nroutes:\n  - port: ${P}\n",
		".env":       "P=1\n",
		".env.local": "P=2\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, found, err := Read(discovery.DirReader{Root: dir})
	if err != nil || !found {
		t.Fatalf("Read = %v, found %v", err, found)
	}
	if len(f.Routes) != 1 || f.Routes[0].Port != 2 {
		t.Fatalf("Routes = %+v", f.Routes)
	}
	f, found, err = Read(discovery.DirReader{Root: t.TempDir()})
	if f != nil || found || err != nil {
		t.Fatalf("empty dir: %v, %v, %v", f, found, err)
	}
}

func TestParseUnknownKeySaysToUpdate(t *testing.T) {
	_, err := Parse([]byte("version: 1\nroutes: []\nextra: 1\n"), nil)
	if err == nil || !strings.Contains(err.Error(), `"extra"`) || !strings.Contains(err.Error(), "update Nearhost") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseUnclosedReferenceInCommentKeepsRoutes(t *testing.T) {
	data := []byte("version: 1\n# fill ${APP_PORT from .env\nroutes:\n  - port: 3000\n    hosts: [app.${D}]\n")
	f, err := Parse(data, map[string]string{"D": "x.io"})
	if err != nil || len(f.Routes) != 1 || f.Routes[0].Hosts[0] != "app.x.io" {
		t.Fatalf("Parse = %+v, %v", f, err)
	}
}

func TestParseFillsVariablesInLabels(t *testing.T) {
	f, err := Parse([]byte("version: 1\nroutes:\n  - port: 1\n    label: ws-${X}\n"), map[string]string{"X": "reverb"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Routes[0].Label; got != "ws-reverb" {
		t.Fatalf("Label = %q, want ws-reverb", got)
	}
}

func TestParseErrorsShowVariablesNotTokens(t *testing.T) {
	_, err := Parse([]byte("version: 1\nroutes:\n  - port: 1\n    hosts: ${H}\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "${H}") || strings.Contains(err.Error(), "nhrcvar") {
		t.Fatalf("err = %v, want the variable ${H} and no placeholder token", err)
	}
}

func TestParseEmptyFileAsksForVersion(t *testing.T) {
	for _, data := range []string{"", "# only a comment\n# and another\n"} {
		_, err := Parse([]byte(data), nil)
		if err == nil || err.Error() != ".nearhostrc needs version: 1" {
			t.Errorf("Parse(%q) = %v, want .nearhostrc needs version: 1", data, err)
		}
	}
}

func TestParseReadsDockerClients(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want bool
	}{
		{"version: 1\ndocker_clients: true\nroutes:\n  - port: 80\n", true},
		{"version: 1\ndocker_clients: false\nroutes:\n  - port: 80\n", false},
		{"version: 1\nroutes:\n  - port: 80\n", false},
	} {
		f, err := Parse([]byte(tc.yaml), nil)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.yaml, err)
		}
		if f.DockerClients != tc.want {
			t.Fatalf("Parse(%q).DockerClients = %v", tc.yaml, f.DockerClients)
		}
	}
	_, err := Parse([]byte("version: 1\ndocker_clients: yes please\nroutes:\n  - port: 80\n"), nil)
	if err == nil || !strings.HasPrefix(err.Error(), ".nearhostrc") {
		t.Fatalf("err = %v", err)
	}
}

func TestHashWithoutDockerClientsIsUnchanged(t *testing.T) {
	body := "routes:\n  - port: 80\n"
	plain, err := Parse([]byte("version: 1\n"+body), nil)
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := json.Marshal(plain.Routes)
	sum := sha256.Sum256(canon)
	if plain.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 = %s", plain.SHA256)
	}
	off, _ := Parse([]byte("version: 1\ndocker_clients: false\n"+body), nil)
	on, _ := Parse([]byte("version: 1\ndocker_clients: true\n"+body), nil)
	if off.SHA256 != plain.SHA256 || on.SHA256 == plain.SHA256 {
		t.Fatalf("off %s on %s plain %s", off.SHA256, on.SHA256, plain.SHA256)
	}
}

func TestParseReadsAlias(t *testing.T) {
	file := []byte("version: 1\nalias: ${APP_HOST:-app.local.x.io}\nroutes:\n  - port: 80\n")
	f, err := Parse(file, map[string]string{"APP_HOST": "mine.local.x.io"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Alias != "mine.local.x.io" {
		t.Fatalf("Alias = %q, want the variable's value", f.Alias)
	}
	if f, err = Parse(file, nil); err != nil || f.Alias != "app.local.x.io" {
		t.Fatalf("Alias = %q, %v, want the default", f.Alias, err)
	}
	if f, err = Parse([]byte("version: 1\nroutes:\n  - port: 80\n"), nil); err != nil || f.Alias != "" {
		t.Fatalf("no alias: %+v, %v", f, err)
	}
	_, err = Parse([]byte("version: 1\nalias: '*.x.io'\nroutes:\n  - port: 80\n"), nil)
	want := ".nearhostrc: alias: " + config.ValidProjectAlias("*.x.io").Error()
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	_, err = Parse([]byte("version: 1\nalias: ${NOPE}\nroutes:\n  - port: 80\n"), nil)
	if err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("err = %v", err)
	}
}

func TestHashKeepsOldFormWithoutAlias(t *testing.T) {
	routes := "routes:\n  - port: 80\n    hosts: [app.local.x.io]\n"
	plain, err := Parse([]byte("version: 1\n"+routes), nil)
	if err != nil {
		t.Fatal(err)
	}
	canon, _ := json.Marshal(plain.Routes)
	sum := sha256.Sum256(canon)
	if plain.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 = %s, want the hash of the routes alone", plain.SHA256)
	}
	// Pinned from a stage 3 build, before alias existed, so an approval of a
	// docker_clients file given then still holds.
	docker, err := Parse([]byte("version: 1\ndocker_clients: true\n"+routes), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "70d75db33943374862f7bd171e72945e3a7db32198718b937e335be795eedf33"; docker.SHA256 != want {
		t.Fatalf("docker_clients SHA256 = %s, want %s", docker.SHA256, want)
	}
	withAlias, err := Parse([]byte("version: 1\nalias: a.test\n"+routes), nil)
	if err != nil {
		t.Fatal(err)
	}
	dockerAlias, err := Parse([]byte("version: 1\nalias: a.test\ndocker_clients: true\n"+routes), nil)
	if err != nil {
		t.Fatal(err)
	}
	if withAlias.SHA256 == plain.SHA256 || dockerAlias.SHA256 == docker.SHA256 || withAlias.SHA256 == dockerAlias.SHA256 {
		t.Fatal("adding alias must change the hash")
	}
}
