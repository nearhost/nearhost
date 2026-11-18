package doctor

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
)

const composeFile = `services:
  laravel.test:
    volumes:
      - .:/var/www/html
  worker:
    environment:
      CALLBACK: https://api.local.x.io/hook
    extra_hosts:
      - "api.local.x.io:host-gateway"
  db:
    image: mysql
`

func dockerProject() config.Project {
	return webProject("app", "app.test", true, "api.local.x.io", "*.status.local.x.io")
}

func dockerCheck(p config.Project, f files, mutate func(*Input)) []Finding {
	in := Input{Project: p, Added: true, Files: f, GOOS: "linux"}
	if mutate != nil {
		mutate(&in)
	}
	return Check(context.Background(), in)
}

func onlyID(fs []Finding, id string) []Finding {
	var out []Finding
	for _, f := range fs {
		if f.ID == id {
			out = append(out, f)
		}
	}
	return out
}

func TestHostGatewayFinding(t *testing.T) {
	f := files{
		"docker-compose.yml": composeFile,
		".env":               "APP_URL=https://api.local.x.io\nSTATUS=acme.status.local.x.io\n",
	}
	got := onlyID(dockerCheck(dockerProject(), f, nil), "docker.host-gateway")
	if len(got) != 1 {
		t.Fatalf("findings = %+v", got)
	}
	g := got[0]
	if g.Severity != SeverityWarning || g.Fix.Kind != FixProject || g.File != "docker-compose.yml" || g.Line != 2 || g.Project != "app" {
		t.Errorf("finding = %+v", g)
	}
	if !strings.Contains(g.Message, "laravel.test") || !strings.Contains(g.Message, "api.local.x.io") || !strings.Contains(g.Message, "acme.status.local.x.io") {
		t.Errorf("message = %q", g.Message)
	}
	if g.Fix.Text != "Map each name to the host in laravel.test's extra_hosts, so the container reaches Nearhost." {
		t.Errorf("text = %q", g.Fix.Text)
	}
	const snippet = `services:
  laravel.test:
    extra_hosts:
      - "acme.status.local.x.io:host-gateway"
      - "api.local.x.io:host-gateway"`
	if g.Fix.Snippet != snippet {
		t.Errorf("snippet = %q", g.Fix.Snippet)
	}
}

func TestHostGatewaySkips(t *testing.T) {
	env := ".env"
	envBody := "APP_URL=https://api.local.x.io\n"

	hostNet := files{"docker-compose.yml": "services:\n  web:\n    network_mode: host\n    volumes:\n      - .:/app\n", env: envBody}
	if got := dockerCheck(dockerProject(), hostNet, func(in *Input) { in.CACert = "/ca.crt" }); len(onlyID(got, "docker.host-gateway"))+len(onlyID(got, "docker.clients"))+len(onlyID(got, "docker.ca")) != 0 {
		t.Errorf("host network: %v", ids(got))
	}

	remote := dockerProject()
	remote.Server = "box"
	f := files{"docker-compose.yml": composeFile, env: envBody}
	for _, id := range ids(dockerCheck(remote, f, func(in *Input) { in.CACert = "/ca.crt" })) {
		if strings.HasPrefix(id, "docker.") {
			t.Errorf("remote project got %s", id)
		}
	}

	for _, id := range ids(dockerCheck(dockerProject(), files{env: envBody}, nil)) {
		if strings.HasPrefix(id, "docker.") {
			t.Errorf("no compose file got %s", id)
		}
	}

	bad := files{"docker-compose.yml": "services: [\n", env: envBody}
	for _, id := range ids(dockerCheck(dockerProject(), bad, nil)) {
		if strings.HasPrefix(id, "docker.") && id != "docker.compose-parse" {
			t.Errorf("bad compose got %s", id)
		}
	}
}

func TestDockerClientsFinding(t *testing.T) {
	f := files{"docker-compose.yml": composeFile}
	// worker references api.local.x.io.
	got := byID(t, dockerCheck(dockerProject(), f, nil), "docker.clients")
	if got.Severity != SeverityWarning || got.Fix.Kind != FixProject || got.Fix.Snippet != "docker_clients: true" ||
		got.Fix.Text != "Add docker_clients: true to .nearhostrc and approve it in Nearhost; with no .nearhostrc yet, `nearhost export` writes one." {
		t.Errorf("linux finding = %+v", got)
	}
	mac := byID(t, dockerCheck(dockerProject(), f, func(in *Input) { in.GOOS = "darwin" }), "docker.clients")
	if mac.Severity != SeverityInfo || !strings.Contains(mac.Fix.Text, "Docker Desktop on macOS already delivers container requests from 127.0.0.1") {
		t.Errorf("darwin finding = %+v", mac)
	}
	win := byID(t, dockerCheck(dockerProject(), f, func(in *Input) { in.GOOS = "windows" }), "docker.clients")
	if win.Severity != SeverityInfo || !strings.Contains(win.Fix.Text, "Docker Desktop on Windows already delivers container requests from 127.0.0.1") {
		t.Errorf("windows finding = %+v", win)
	}

	approved := dockerProject()
	approved.Nearhostrc = &config.RcSnapshot{SHA256: "h", DockerClients: true}
	if has(dockerCheck(approved, f, nil), "docker.clients") {
		t.Error("reported despite the opt-in")
	}

	if has(dockerCheck(dockerProject(), files{"docker-compose.yml": "services:\n  db:\n    image: mysql\n"}, nil), "docker.clients") {
		t.Error("reported with no reference")
	}
}

func TestCAFinding(t *testing.T) {
	const ca = "/Users/me/Library/Application Support/nearhost/caddy/pki/authorities/local/root.crt"
	run := func(compose string) []Finding {
		return onlyID(dockerCheck(dockerProject(), files{"docker-compose.yml": compose}, func(in *Input) { in.CACert = ca }), "docker.ca")
	}
	const base = "services:\n  worker:\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n"

	got := run(base)
	if len(got) != 1 {
		t.Fatalf("findings = %+v", got)
	}
	g := got[0]
	if g.Severity != SeverityWarning || g.Fix.Kind != FixProject || g.File != "docker-compose.yml" || g.Line != 2 {
		t.Errorf("finding = %+v", g)
	}
	for _, s := range []string{ca, "NEARHOST_CA_CERT", "shell profile", "in .env when the project doesn't commit .env",
		"SSL_CERT_FILE", "AWS_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "update-ca-certificates"} {
		if !strings.Contains(g.Fix.Text, s) {
			t.Errorf("text lacks %q: %s", s, g.Fix.Text)
		}
	}
	// Compose reads neither .env.local nor any file but .env.
	if strings.Contains(g.Fix.Text, ".env.local") {
		t.Errorf("text names .env.local: %s", g.Fix.Text)
	}
	for _, s := range []string{`"${NEARHOST_CA_CERT:?set NEARHOST_CA_CERT to Nearhost's root certificate}:/usr/local/share/ca-certificates/nearhost.crt:ro"`, "NODE_EXTRA_CA_CERTS: /usr/local/share/ca-certificates/nearhost.crt"} {
		if !strings.Contains(g.Fix.Snippet, s) {
			t.Errorf("snippet lacks %q: %s", s, g.Fix.Snippet)
		}
	}

	if len(run(base+"      NODE_EXTRA_CA_CERTS: /x\n")) != 0 {
		t.Error("reported with NODE_EXTRA_CA_CERTS set")
	}
	if len(run(base+"    volumes:\n      - \""+ca+":/c:ro\"\n")) != 0 {
		t.Error("reported with the certificate mounted")
	}

	// The snippet as written, with the variable set where Compose reads it.
	mounted := base + "    volumes:\n      - \"${NEARHOST_CA_CERT:?set NEARHOST_CA_CERT to Nearhost's root certificate}:/c:ro\"\n"
	withEnv := func(name string) []Finding {
		f := files{"docker-compose.yml": mounted, name: "NEARHOST_CA_CERT=\"" + ca + "\"\n"}
		return onlyID(dockerCheck(dockerProject(), f, func(in *Input) { in.CACert = ca }), "docker.ca")
	}
	if got := withEnv(".env"); len(got) != 0 {
		t.Errorf("reported with the snippet and NEARHOST_CA_CERT in .env: %+v", got)
	}
	if got := withEnv(".env.local"); len(got) != 1 {
		t.Errorf("not reported with NEARHOST_CA_CERT only in .env.local, which Compose doesn't read: %+v", got)
	}
	plain := files{"docker-compose.yml": base + "    volumes:\n      - \"${NEARHOST_CA_CERT}:/c:ro\"\n", ".env.local": "NEARHOST_CA_CERT=\"" + ca + "\"\n"}
	if got := onlyID(dockerCheck(dockerProject(), plain, func(in *Input) { in.CACert = ca }), "docker.ca"); len(got) != 1 {
		t.Errorf("not reported with ${NEARHOST_CA_CERT} set only in .env.local: %+v", got)
	}
	long := base + "    volumes:\n      - type: bind\n        source: ${NEARHOST_CA_CERT?unset}\n        target: /c\n"
	if got := onlyID(dockerCheck(dockerProject(), files{"docker-compose.yml": long, ".env": "NEARHOST_CA_CERT=" + ca[:5] + "\n"},
		func(in *Input) { in.CACert = ca[:5] }), "docker.ca"); len(got) != 0 {
		t.Errorf("reported with ${VAR?msg} set in .env: %+v", got)
	}

	none := dockerCheck(dockerProject(), files{"docker-compose.yml": base}, nil)
	if has(none, "docker.ca") {
		t.Error("reported with no CACert")
	}
}

func TestHostGatewayHonoursMergedExtraHosts(t *testing.T) {
	const compose = `x-common: &common
  extra_hosts:
    - "api.local.x.io:host-gateway"
services:
  worker:
    <<: *common
    environment:
      CALLBACK: https://api.local.x.io/hook
`
	if has(dockerCheck(dockerProject(), files{"docker-compose.yml": compose}, nil), "docker.host-gateway") {
		t.Error("reported although extra_hosts is merged in")
	}
}

// extra_hosts in the override file, which Compose merges over the Compose
// file, maps the name for the service the Compose file defines.
func TestHostGatewayHonoursTheOverride(t *testing.T) {
	f := files{
		"compose.yaml":          "services:\n  worker:\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n",
		"compose.override.yaml": "services:\n  worker:\n    extra_hosts:\n      - \"api.local.x.io:host-gateway\"\n",
	}
	if got := dockerCheck(dockerProject(), f, nil); has(got, "docker.host-gateway") {
		t.Errorf("reported although the override maps the name: %+v", onlyID(got, "docker.host-gateway"))
	}
}

// The fix for a service goes in the override when the override names the
// service, and in the Compose file when it does not.
func TestHostGatewayNamesTheFileToEdit(t *testing.T) {
	f := files{
		"docker-compose.yml":          "services:\n  worker:\n    image: x\n  api:\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n",
		"docker-compose.override.yml": "services:\n  db:\n    image: y\n\n  worker:\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n",
	}
	got := onlyID(dockerCheck(dockerProject(), f, nil), "docker.host-gateway")
	if len(got) != 2 {
		t.Fatalf("findings = %+v", got)
	}
	byService := map[string]Finding{}
	for _, g := range got {
		byService[strings.Fields(g.Message)[0]] = g
	}
	if g := byService["api"]; g.File != "docker-compose.yml" || g.Line != 4 {
		t.Errorf("api: file %s line %d, want docker-compose.yml line 4", g.File, g.Line)
	}
	if g := byService["worker"]; g.File != "docker-compose.override.yml" || g.Line != 5 {
		t.Errorf("worker: file %s line %d, want docker-compose.override.yml line 5", g.File, g.Line)
	}
}

// The override's environment replaces the Compose file's by key, in the
// map form and the list form alike.
func TestOverrideEnvironmentReplacesByKey(t *testing.T) {
	const listed = "    environment:\n      - CALLBACK=%s\n"
	const mapped = "    environment:\n      CALLBACK: %s\n"
	for _, c := range []struct{ name, base, override string }{
		{"map over list", listed, mapped},
		{"list over map", mapped, listed},
	} {
		f := files{
			"compose.yaml":          "services:\n  worker:\n" + fmt.Sprintf(c.base, "https://api.local.x.io/hook"),
			"compose.override.yaml": "services:\n  worker:\n" + fmt.Sprintf(c.override, "https://example.com/hook"),
		}
		if got := dockerCheck(dockerProject(), f, nil); has(got, "docker.host-gateway") {
			t.Errorf("%s: the override's value did not replace the Compose file's: %+v", c.name, onlyID(got, "docker.host-gateway"))
		}
		f["compose.override.yaml"] = "services:\n  worker:\n" + fmt.Sprintf(c.override, "https://api.local.x.io/hook")
		f["compose.yaml"] = "services:\n  worker:\n" + fmt.Sprintf(c.base, "https://example.com/hook")
		if got := dockerCheck(dockerProject(), f, nil); !has(got, "docker.host-gateway") {
			t.Errorf("%s: the override's value was not checked", c.name)
		}
	}
}

// COMPOSE_FILE points Compose at files Nearhost does not read, and doctor
// says so once: beside a Compose file, or when the project's .env sets it.
// One from the shell alone, in a project with no Compose file, is not the
// project's.
func TestComposeFileIsNoted(t *testing.T) {
	f := files{"compose.yaml": composeFile, ".env": "COMPOSE_FILE=compose.yaml:compose.dev.yaml\n"}
	got := onlyID(dockerCheck(dockerProject(), f, nil), "docker.compose-file")
	if len(got) != 1 || got[0].Severity != SeverityInfo || !strings.HasSuffix(got[0].Message, "it read compose.yaml") {
		t.Fatalf("findings = %+v", got)
	}
	if has(dockerCheck(dockerProject(), files{"compose.yaml": composeFile}, nil), "docker.compose-file") {
		t.Error("reported with no COMPOSE_FILE")
	}
	shell := func(in *Input) { in.Environ = map[string]string{"COMPOSE_FILE": "dev.yaml"} }
	if !has(dockerCheck(dockerProject(), files{"compose.yaml": composeFile}, shell), "docker.compose-file") {
		t.Error("not reported with the shell's COMPOSE_FILE beside a Compose file")
	}
	if has(dockerCheck(dockerProject(), files{}, shell), "docker.compose-file") {
		t.Error("reported with the shell's COMPOSE_FILE and no Compose file")
	}
	got = onlyID(dockerCheck(dockerProject(), files{".env": "COMPOSE_FILE=dev.yaml\n"}, nil), "docker.compose-file")
	if len(got) != 1 || !strings.HasSuffix(got[0].Message, "it found none of them") {
		t.Errorf("project .env with no Compose file: %+v", got)
	}
}

// Compose's merge tags in the override: !reset clears a key or removes the
// service, !override replaces a key, and an environment key tagged !reset
// is unset.
func TestOverrideMergeTags(t *testing.T) {
	const base = "services:\n  worker:\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n" +
		"    extra_hosts:\n      - \"api.local.x.io:host-gateway\"\n"
	cases := []struct {
		name, override string
		want           bool // docker.host-gateway reported
	}{
		{"no tags", "services:\n  worker:\n    image: x\n", false},
		{"extra_hosts reset to a list", "services:\n  worker:\n    extra_hosts: !reset []\n", true},
		{"extra_hosts reset to null", "services:\n  worker:\n    extra_hosts: !reset null\n", true},
		{"extra_hosts overridden", "services:\n  worker:\n    extra_hosts: !override\n      - \"other.x.io:host-gateway\"\n", true},
		{"environment key reset", "services:\n  worker:\n    environment:\n      CALLBACK: !reset null\n    extra_hosts: !reset []\n", false},
		{"environment reset", "services:\n  worker:\n    environment: !reset {}\n    extra_hosts: !reset []\n", false},
		{"environment overridden", "services:\n  worker:\n    environment: !override\n      OTHER: x\n    extra_hosts: !reset []\n", false},
		{"service reset", "services:\n  worker: !reset null\n", false},
		{"network_mode host", "services:\n  worker:\n    network_mode: host\n    extra_hosts: !reset []\n", false},
	}
	for _, c := range cases {
		f := files{"compose.yaml": base, "compose.override.yaml": c.override}
		if got := has(dockerCheck(dockerProject(), f, nil), "docker.host-gateway"); got != c.want {
			t.Errorf("%s: host-gateway reported = %v, want %v", c.name, got, c.want)
		}
	}

	// network_mode: !reset in the override undoes the Compose file's host mode.
	f := files{
		"compose.yaml":          "services:\n  worker:\n    network_mode: host\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n",
		"compose.override.yaml": "services:\n  worker:\n    network_mode: !reset null\n",
	}
	if !has(dockerCheck(dockerProject(), f, nil), "docker.host-gateway") {
		t.Error("network_mode reset: host-gateway not reported")
	}

	// env_file and volumes: !reset drops the Compose file's.
	f = files{
		"compose.yaml":          "services:\n  worker:\n    env_file: app.env\n",
		"compose.override.yaml": "services:\n  worker:\n    env_file: !reset null\n",
		"app.env":               "CALLBACK=https://api.local.x.io/hook\n",
	}
	if has(dockerCheck(dockerProject(), f, nil), "docker.host-gateway") {
		t.Error("env_file reset: the dropped env_file was read")
	}
	f = files{
		"compose.yaml":          "services:\n  worker:\n    volumes:\n      - .:/app\n",
		"compose.override.yaml": "services:\n  worker:\n    volumes: !override\n      - ./data:/data\n",
		".env":                  "CALLBACK=https://api.local.x.io/hook\n",
	}
	if has(dockerCheck(dockerProject(), f, nil), "docker.host-gateway") {
		t.Error("volumes overridden: the dropped project mount was used")
	}
}

// An override that is not valid YAML is named in a finding, and the
// Compose file's services go unchecked rather than half-checked.
func TestBadOverrideIsReported(t *testing.T) {
	f := files{"compose.yaml": composeFile, "compose.override.yaml": "services: [\n", ".env": "APP_URL=https://api.local.x.io\n"}
	got := dockerCheck(dockerProject(), f, nil)
	p := onlyID(got, "docker.compose-parse")
	if len(p) != 1 || p[0].File != "compose.override.yaml" || p[0].Severity != SeverityWarning ||
		p[0].Message != "compose.override.yaml is not valid YAML, so Nearhost did not check the project's Compose services" {
		t.Fatalf("findings = %+v", got)
	}
	if has(got, "docker.host-gateway") {
		t.Errorf("services checked although the override did not parse: %v", ids(got))
	}
}

func TestCAFindingHonoursMergedEnvironment(t *testing.T) {
	const compose = `x-common: &common
  NODE_EXTRA_CA_CERTS: /ca.crt
services:
  worker:
    environment:
      <<: *common
      CALLBACK: https://api.local.x.io/hook
`
	got := dockerCheck(dockerProject(), files{"docker-compose.yml": compose}, func(in *Input) { in.CACert = "/ca.crt" })
	if has(got, "docker.ca") {
		t.Error("docker.ca reported although the CA variable is merged in")
	}
}

// The fix tells developers to set NEARHOST_CA_CERT in their shell profile, so
// a value from the shell counts as Compose counts it: over .env's, and still
// never from .env.local.
func TestCAFindingReadsTheShell(t *testing.T) {
	const ca = "/home/me/.local/share/nearhost/root.crt"
	const compose = "services:\n  worker:\n    environment:\n      CALLBACK: https://api.local.x.io/hook\n" +
		"    volumes:\n      - \"${NEARHOST_CA_CERT:?set NEARHOST_CA_CERT to Nearhost's root certificate}:/c:ro\"\n"
	run := func(f files, environ map[string]string) []Finding {
		f["docker-compose.yml"] = compose
		return onlyID(dockerCheck(dockerProject(), f, func(in *Input) {
			in.CACert = ca
			in.Environ = environ
		}), "docker.ca")
	}

	if got := run(files{}, map[string]string{"NEARHOST_CA_CERT": ca}); len(got) != 0 {
		t.Errorf("reported with NEARHOST_CA_CERT set in the shell: %+v", got)
	}
	if got := run(files{".env.local": "NEARHOST_CA_CERT=" + ca + "\n"}, nil); len(got) != 1 {
		t.Errorf("not reported with NEARHOST_CA_CERT only in .env.local and no shell: %+v", got)
	}
	if got := run(files{".env": "NEARHOST_CA_CERT=/elsewhere.crt\n"}, map[string]string{"NEARHOST_CA_CERT": ca}); len(got) != 0 {
		t.Errorf("reported with the shell's NEARHOST_CA_CERT overriding .env's: %+v", got)
	}
	if got := run(files{".env": "NEARHOST_CA_CERT=" + ca + "\n"}, map[string]string{"NEARHOST_CA_CERT": "/elsewhere.crt"}); len(got) != 1 {
		t.Errorf("not reported with .env's NEARHOST_CA_CERT overridden by the shell's: %+v", got)
	}
}

// readLog is a files that records the names it was asked to read.
type readLog struct {
	files
	read []string
}

func (r *readLog) ReadFile(name string) ([]byte, error) {
	r.read = append(r.read, name)
	return r.files.ReadFile(name)
}

// An env_file outside the project is never read: the Compose file arrives
// with the repository and must not point doctor at the rest of the machine.
func TestEnvFileOutsideTheProjectIsNotRead(t *testing.T) {
	const compose = "services:\n  worker:\n    env_file:\n      - ../../x\n      - /etc/x\n      - sub/../../y\n" +
		"      - '..\\..\\x'\n      - 'C:\\x'\n      - 'c:x'\n      - '\\\\host\\share\\x'\n"
	const env = "CALLBACK=https://api.local.x.io/hook\n"
	r := &readLog{files: files{
		"docker-compose.yml": compose,
		"../../x":            env,
		"/etc/x":             env,
		"../y":               env,
		`..\..\x`:            env,
		"C:/x":               env,
		`C:\x`:               env,
		"c:x":                env,
		"//host/share/x":     env,
		"/host/share/x":      env,
		`\\host\share\x`:     env,
	}}
	in := Input{Project: dockerProject(), Added: true, Files: r, GOOS: "linux"}
	got := Check(context.Background(), in)
	if has(got, "docker.host-gateway") {
		t.Errorf("a file outside the project was used: %+v", got)
	}
	for _, name := range r.read {
		if !slices.Contains([]string{"compose.yaml", "compose.yml", "docker-compose.yml", "compose.override.yml", "compose.override.yaml",
			"docker-compose.override.yml", "docker-compose.override.yaml", ".env", ".env.local", ".nearhostrc"}, name) {
			t.Errorf("read %s, outside the project", name)
		}
	}
}

// A chain of << merges deeper than doctor follows stops the walk and says so.
func TestComposeNestedTooDeep(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-0: &a0\n  CALLBACK: https://api.local.x.io/hook\n")
	for i := 1; i <= 70; i++ {
		fmt.Fprintf(&b, "x-%d: &a%d\n  <<: *a%d\n", i, i, i-1)
	}
	b.WriteString("services:\n  worker:\n    environment:\n      <<: *a70\n")
	got := onlyID(dockerCheck(dockerProject(), files{"docker-compose.yml": b.String()}, nil), "docker.compose-depth")
	if len(got) != 1 {
		t.Fatalf("findings = %+v", got)
	}
	g := got[0]
	if g.File != "docker-compose.yml" || g.Severity != SeverityWarning || g.Fix.Kind != FixProject ||
		g.Message != "docker-compose.yml nests or repeats << merges too much to check, so Nearhost did not check its services" {
		t.Errorf("finding = %+v", g)
	}
}

// A chain of merges within the limit is followed to its end.
func TestComposeNestedWithinTheLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-0: &a0\n  CALLBACK: https://api.local.x.io/hook\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&b, "x-%d: &a%d\n  <<: *a%d\n", i, i, i-1)
	}
	b.WriteString("services:\n  worker:\n    environment:\n      <<: *a60\n")
	got := dockerCheck(dockerProject(), files{"docker-compose.yml": b.String()}, nil)
	if has(got, "docker.compose-depth") || !has(got, "docker.host-gateway") {
		t.Errorf("findings = %+v", ids(got))
	}
}

// Merges that repeat an anchor double the walk at each level, so 30 levels
// stay under the depth limit and would still never finish. The walk stops
// at its lookup budget and says so.
func TestComposeRepeatedMergesStop(t *testing.T) {
	var b strings.Builder
	b.WriteString("x-0: &a0\n  CALLBACK: https://api.local.x.io/hook\n")
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "x-%d: &a%d\n  <<: [*a%d, *a%d]\n", i, i, i-1, i-1)
	}
	b.WriteString("services:\n  worker:\n    environment:\n      <<: *a30\n      OTHER: x\n")
	done := make(chan []Finding, 1)
	go func() { done <- dockerCheck(dockerProject(), files{"docker-compose.yml": b.String()}, nil) }()
	select {
	case got := <-done:
		if len(onlyID(got, "docker.compose-depth")) != 1 {
			t.Fatalf("findings = %+v", ids(got))
		}
	case <-time.After(10 * time.Second):
		panic(t.Name() + ": the walk of repeated merges did not stop")
	}
}
