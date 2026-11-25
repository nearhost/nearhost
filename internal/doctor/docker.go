package doctor

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hostname"
	"github.com/nearhost/nearhost/internal/report"
)

// hostToken matches a dotted name inside a value such as a URL.
var hostToken = regexp.MustCompile(`[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)

// caVariables are the variables that point a client at a certificate bundle.
// A service that sets one is taken to have dealt with the local CA.
var caVariables = []string{"SSL_CERT_FILE", "NODE_EXTRA_CA_CERTS", "AWS_CA_BUNDLE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE"}

const caMountPath = "/usr/local/share/ca-certificates/nearhost.crt"

// dockerFindings reports the three ways a container that calls a name Nearhost
// serves can fail to reach it: the name does not point at the host, the
// request arrives from a Docker address Nearhost does not accept, or the
// container does not trust the local CA. Only a project on this machine is
// checked, as a container on an SSH server cannot reach the proxy here.
func dockerFindings(in Input) []Finding {
	p := in.Project
	if p.Server != config.LocalServer {
		return nil
	}
	dotEnv := composeEnv(in.Files, in.Environ)
	var out []Finding
	compose, err := discovery.ReadCompose(in.Files)
	if err != nil {
		return nil
	}
	// A COMPOSE_FILE from the shell profile applies to every project, so
	// it is worth a note only where it can matter: beside a Compose file,
	// or in the project's own .env.
	if dotEnv["COMPOSE_FILE"] != "" && (len(compose) > 0 || projectSetsComposeFile(in.Files)) {
		out = append(out, composeFileFinding(compose))
	}
	if len(compose) == 0 {
		return out
	}
	env, err := discovery.ReadEnv(in.Files)
	if err != nil {
		env = nil
	}
	// Compose merges the override file over the Compose file, so the
	// checks look at the services as merged.
	var services []service
	for _, f := range compose {
		fileServices, err := composeServices(f.Data, env, dotEnv)
		if errors.Is(err, errComposeTooDeep) {
			return append(out, composeDepthFinding(f.Name))
		}
		if err != nil {
			return append(out, composeParseFinding(f.Name))
		}
		for i := range fileServices {
			fileServices[i].File = f.Name
		}
		services = mergeServices(services, fileServices)
	}

	referenced := false
	for _, s := range services {
		if s.HostNetwork {
			continue
		}
		refs := serviceReferences(in, s, env)
		if len(refs) == 0 {
			continue
		}
		referenced = true

		if missing := slices.DeleteFunc(slices.Clone(refs), func(n string) bool { return slices.Contains(s.ExtraHosts, n) }); len(missing) > 0 {
			out = append(out, hostGatewayFinding(s, missing))
		}
		if f, ok := caFinding(in.CACert, s); ok {
			out = append(out, f)
		}
	}
	if referenced && !p.DockerClients() {
		out = append(out, dockerClientsFinding(in.GOOS))
	}
	return out
}

// serviceReferences lists, sorted, the names Nearhost serves that the service
// is configured with: in its environment, in its env_file files, and, when it
// mounts the project directory, in the project's own env, which the app reads
// from there.
func serviceReferences(in Input, s service, projectEnv map[string]string) []string {
	var values []string
	for _, v := range s.Env {
		values = append(values, v)
	}
	for _, f := range s.EnvFiles {
		name, ok := insideProject(f)
		if !ok {
			continue
		}
		data, err := in.Files.ReadFile(name)
		if err != nil {
			continue
		}
		if e, err := discovery.ParseEnv(data); err == nil {
			for _, v := range e {
				values = append(values, v)
			}
		}
	}
	if slices.ContainsFunc(s.Sources, func(src string) bool { return src == "." || src == "./" }) {
		for _, v := range projectEnv {
			values = append(values, v)
		}
	}

	exact := exactNames(in.Project)
	patterns := patternsOf(in.Project)
	var out []string
	for _, v := range values {
		for _, tok := range hostToken.FindAllString(v, -1) {
			tok = strings.ToLower(tok)
			if slices.Contains(out, tok) {
				continue
			}
			if slices.Contains(exact, tok) || slices.ContainsFunc(patterns, func(pat string) bool { return hostname.Match(pat, tok) }) {
				out = append(out, tok)
			}
		}
	}
	sort.Strings(out)
	return out
}

// insideProject cleans p, a path from the Compose file, and reports whether
// it stays inside the project. One that leaves it is never read: the
// Compose file comes with the repository, and must not point doctor at
// files elsewhere on the machine. A backslash counts as a separator and a
// drive (C:) or UNC start (\\host) is refused on every OS, as a file
// written on Windows may be checked anywhere.
func insideProject(p string) (string, bool) {
	p = strings.ReplaceAll(p, `\`, "/")
	if len(p) >= 2 && p[1] == ':' && isASCIILetter(p[0]) {
		return "", false
	}
	p = path.Clean(p)
	if path.IsAbs(p) || p == ".." || strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

func isASCIILetter(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// composeDepthFinding reports a Compose file whose << merges nest deeper,
// or repeat more, than doctor follows.
func composeDepthFinding(file string) Finding {
	return Finding{
		ID:       string(report.DockerComposeDepth),
		Severity: SeverityWarning,
		Message:  file + " nests or repeats << merges too much to check, so Nearhost did not check its services",
		File:     file,
		Fix: Fix{
			Kind: FixProject,
			Text: fmt.Sprintf("Keep each chain of << merges in %s to %d levels or fewer, merge each anchor once per level, then run nearhost doctor again.", file, maxMergeDepth),
		},
	}
}

// hostGatewayFinding reports a service that calls names Nearhost serves
// with no extra_hosts entry that points them at the host. The fix goes in
// the file that holds the service, the override when it names it.
func hostGatewayFinding(s service, missing []string) Finding {
	var b strings.Builder
	fmt.Fprintf(&b, "services:\n  %s:\n    extra_hosts:", s.Name)
	for _, n := range missing {
		fmt.Fprintf(&b, "\n      - \"%s:host-gateway\"", n)
	}
	return Finding{
		ID:       string(report.DockerHostGateway),
		Severity: SeverityWarning,
		Message: fmt.Sprintf("%s uses %s, which a container resolves to itself or to the public internet, not to Nearhost",
			s.Name, strings.Join(missing, ", ")),
		File: s.File,
		Line: s.Line,
		Fix: Fix{
			Kind:    FixProject,
			Text:    "Map each name to the host in " + s.Name + "'s extra_hosts, so the container reaches Nearhost.",
			Snippet: b.String(),
		},
	}
}

// projectSetsComposeFile reports whether the project's .env sets
// COMPOSE_FILE.
func projectSetsComposeFile(r discovery.FileReader) bool {
	env, err := discovery.ReadComposeEnv(r)
	return err == nil && env["COMPOSE_FILE"] != ""
}

// composeParseFinding reports a Compose file that is not valid YAML, so
// doctor could not check its services. The parser's text is left out, as
// it can quote the file.
func composeParseFinding(file string) Finding {
	return Finding{
		ID:       string(report.DockerComposeParse),
		Severity: SeverityWarning,
		Message:  file + " is not valid YAML, so Nearhost did not check the project's Compose services",
		File:     file,
		Fix: Fix{
			Kind: FixProject,
			Text: "Fix " + file + " until docker compose config reads it without an error, then run nearhost doctor again.",
		},
	}
}

// composeFileFinding says that COMPOSE_FILE is set, in .env or the shell,
// and that Nearhost read the default files, read, and not the ones it
// names.
func composeFileFinding(read []discovery.ComposeFile) Finding {
	what := "found none of them"
	if len(read) > 0 {
		names := make([]string, 0, len(read))
		for _, f := range read {
			names = append(names, f.Name)
		}
		what = "read " + strings.Join(names, " and ")
	}
	return Finding{
		ID:       string(report.DockerComposeFile),
		Severity: SeverityInfo,
		Message:  "COMPOSE_FILE is set, and Nearhost checks only the Compose files Compose reads by default, not the ones COMPOSE_FILE names: it " + what,
		Fix: Fix{
			Kind: FixManual,
			Text: "Check the services in the files COMPOSE_FILE names yourself: a service that calls a name Nearhost serves needs that name mapped to host-gateway in its extra_hosts.",
		},
	}
}

func dockerClientsFinding(goos string) Finding {
	sev := SeverityWarning
	text := "Add docker_clients: true to .nearhostrc and approve it in Nearhost; with no .nearhostrc yet, `nearhost export` writes one."
	switch goos {
	case "darwin":
		sev = SeverityInfo
		text += " Docker Desktop on macOS already delivers container requests from 127.0.0.1, so this matters on Linux."
	case "windows":
		sev = SeverityInfo
		text += " Docker Desktop on Windows already delivers container requests from 127.0.0.1, so this matters on Linux."
	}
	return Finding{
		ID:       string(report.DockerClients),
		Severity: sev,
		Message:  "a container calls a name Nearhost serves, and Nearhost does not accept requests from Docker's network",
		Fix:      Fix{Kind: FixProject, Text: text, Snippet: "docker_clients: true"},
	}
}

// composeEnv is what Compose fills in variables from: the project's .env,
// the only file it reads, with the shell's environ over it, as Compose
// orders them. A missing or broken .env gives none of its own.
func composeEnv(r discovery.FileReader, environ map[string]string) map[string]string {
	env := map[string]string{}
	if data, err := r.ReadFile(".env"); err == nil {
		if dot, err := discovery.ParseEnv(data); err == nil {
			maps.Copy(env, dot)
		}
	}
	maps.Copy(env, environ)
	return env
}

// caFinding reports a service that calls a served name over HTTPS without
// trusting Nearhost's root certificate.
func caFinding(caCert string, s service) (Finding, bool) {
	if caCert == "" {
		return Finding{}, false
	}
	for _, v := range caVariables {
		if _, ok := s.Env[v]; ok {
			return Finding{}, false
		}
	}
	if slices.Contains(s.Sources, caCert) {
		return Finding{}, false
	}
	snippet := fmt.Sprintf("services:\n  %s:\n    environment:\n      NODE_EXTRA_CA_CERTS: %s\n    volumes:\n      - \"${NEARHOST_CA_CERT:?set NEARHOST_CA_CERT to Nearhost's root certificate}:%s:ro\"",
		s.Name, caMountPath, caMountPath)
	return Finding{
		ID:       string(report.DockerCA),
		Severity: SeverityWarning,
		Message:  s.Name + " calls a name Nearhost serves, and does not trust Nearhost's root certificate",
		File:     s.File,
		Line:     s.Line,
		Fix: Fix{
			Kind: FixProject,
			Text: "Mount Nearhost's root certificate, " + caCert + ", and point the service at it. " +
				"Each developer sets NEARHOST_CA_CERT to that path in their shell profile, or in .env when the project doesn't commit .env, " +
				"so the Compose file holds no one's path; Compose reads only .env and the shell. " +
				"SSL_CERT_FILE, AWS_CA_BUNDLE and REQUESTS_CA_BUNDLE replace the default bundle: " +
				"point them at a bundle that includes the certificate, or have the image run update-ca-certificates.",
			Snippet: snippet,
		},
	}, true
}
