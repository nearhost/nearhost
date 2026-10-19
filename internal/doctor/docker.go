package doctor

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/hostname"
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
	file, data, err := discovery.ReadCompose(in.Files)
	if err != nil || file == "" {
		return nil
	}
	env, err := discovery.ReadEnv(in.Files)
	if err != nil {
		env = nil
	}
	services, err := composeServices(data, env)
	if err != nil {
		// discovery reports a Compose file that does not parse.
		return nil
	}

	var out []Finding
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
			out = append(out, hostGatewayFinding(file, s, missing))
		}
		if f, ok := caFinding(in.CACert, file, s); ok {
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
		data, err := in.Files.ReadFile(path.Clean(f))
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

func hostGatewayFinding(file string, s service, missing []string) Finding {
	var b strings.Builder
	fmt.Fprintf(&b, "services:\n  %s:\n    extra_hosts:", s.Name)
	for _, n := range missing {
		fmt.Fprintf(&b, "\n      - \"%s:host-gateway\"", n)
	}
	return Finding{
		ID:       "docker.host-gateway",
		Severity: SeverityWarning,
		Message: fmt.Sprintf("%s uses %s, which a container resolves to itself or to the public internet, not to Nearhost",
			s.Name, strings.Join(missing, ", ")),
		File: file,
		Line: s.Line,
		Fix: Fix{
			Kind:    FixProject,
			Text:    "Map each name to the host in " + s.Name + "'s extra_hosts, so the container reaches Nearhost.",
			Snippet: b.String(),
		},
	}
}

func dockerClientsFinding(goos string) Finding {
	sev := SeverityWarning
	text := "Add docker_clients: true to .nearhostrc and approve it in Nearhost; with no .nearhostrc yet, `nearhost export` writes one."
	if goos == "darwin" {
		sev = SeverityInfo
		text += " Docker Desktop on macOS already delivers container requests from 127.0.0.1, so this matters on Linux."
	}
	return Finding{
		ID:       "docker.clients",
		Severity: sev,
		Message:  "a container calls a name Nearhost serves, and Nearhost does not accept requests from Docker's network",
		Fix:      Fix{Kind: FixProject, Text: text, Snippet: "docker_clients: true"},
	}
}

// caFinding reports a service that calls a served name over HTTPS without
// trusting Nearhost's root certificate.
func caFinding(caCert, file string, s service) (Finding, bool) {
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
	snippet := fmt.Sprintf("services:\n  %s:\n    environment:\n      NODE_EXTRA_CA_CERTS: %s\n    volumes:\n      - \"${NEARHOST_CA_CERT}:%s:ro\"",
		s.Name, caMountPath, caMountPath)
	return Finding{
		ID:       "docker.ca",
		Severity: SeverityWarning,
		Message:  s.Name + " calls a name Nearhost serves, and does not trust Nearhost's root certificate",
		File:     file,
		Line:     s.Line,
		Fix: Fix{
			Kind: FixProject,
			Text: "Mount Nearhost's root certificate, " + caCert + ", and point the service at it. " +
				"Each developer sets NEARHOST_CA_CERT= to that path in their own .env.local, so the Compose file holds no one's path. " +
				"SSL_CERT_FILE, AWS_CA_BUNDLE and REQUESTS_CA_BUNDLE replace the default bundle: " +
				"point them at a bundle that includes the certificate, or have the image run update-ca-certificates.",
			Snippet: snippet,
		},
	}, true
}
