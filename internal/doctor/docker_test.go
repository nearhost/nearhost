package doctor

import (
	"context"
	"strings"
	"testing"

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
		if strings.HasPrefix(id, "docker.") {
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
