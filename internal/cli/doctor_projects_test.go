package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/doctor"
	"github.com/nearhost/nearhost/internal/proxy"
)

// memFiles is a server's files held in memory: dirs maps a directory to the
// files in it.
type memFiles struct {
	dirs   map[string]map[string]string
	closed int
	// block, when not nil, holds every read until it is closed.
	block chan struct{}
}

func (m *memFiles) resolve(p string) (string, error) {
	if _, ok := m.dirs[p]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s: no such directory", p)
}

func (m *memFiles) listDirs(string) ([]string, error) { return nil, nil }

func (m *memFiles) reader(dir string) discovery.FileReader { return memReader{m, dir} }

func (m *memFiles) close() error { m.closed++; return nil }

type memReader struct {
	m   *memFiles
	dir string
}

func (r memReader) ReadFile(name string) ([]byte, error) {
	if r.m.block != nil {
		<-r.m.block
	}
	if data, ok := r.m.dirs[r.dir][name]; ok {
		return []byte(data), nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// writeFiles writes files, by name, into dir.
func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// withProjects gives b a project section: this machine's files are the
// temporary directories the tests write, every other server's are servers',
// and every port has a listener.
func withProjects(b doctorBackend, servers map[string]serverFiles, opened *[]string) doctorBackend {
	b.openFiles = func(_ context.Context, srv config.Server) (serverFiles, error) {
		*opened = append(*opened, srv.Name)
		if srv.Name == config.LocalServer {
			return localFiles{}, nil
		}
		if f, ok := servers[srv.Name]; ok {
			return f, nil
		}
		return nil, fmt.Errorf("connect to %s: no route to host", srv.SSHHost)
	}
	b.env.listening = func(context.Context, int) bool { return true }
	return b
}

const unknownKeyRc = "version: 1\nroutes: []\nextra: true\n"

// shopAndBlog saves a config with two enabled local projects, shop with a
// .nearhostrc this Nearhost does not understand and blog with nothing wrong,
// and a disabled one whose file is broken.
func shopAndBlog(t *testing.T) string {
	t.Helper()
	path := configHome(t)
	shop, blog, old := localProject("shop", 3000, true), localProject("blog", 8000, true), localProject("old", 8001, false)
	shop.Path, blog.Path, old.Path = t.TempDir(), t.TempDir(), t.TempDir()
	writeFiles(t, shop.Path, map[string]string{".nearhostrc": unknownKeyRc})
	writeFiles(t, old.Path, map[string]string{".nearhostrc": "version: [\n"})
	saveConfig(t, path, &config.Config{Projects: []config.Project{shop, blog, old}})
	return path
}

const shopAndBlogHosts = "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 blog.test\n127.0.0.1 old.test\n127.0.0.1 shop.test\n# nearhost:end\n"

func TestDoctorProjectSection(t *testing.T) {
	path := shopAndBlog(t)
	f := healthy()
	f.hosts = shopAndBlogHosts
	var opened []string
	out, err := execute(t, newDoctorCmd(withProjects(f.backend(), nil, &opened)), "doctor")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	want := "" +
		"ok    config           " + path + "\n" +
		"ok    port 443         free\n" +
		"ok    port 80          free\n" +
		"ok    port 6327        free\n" +
		"ok    local CA         trusted by the system trust store\n" +
		"ok    browser stores   the NSS stores trust the CA\n" +
		"ok    hosts block      3 names\n" +
		"\n" +
		"Projects\n" +
		"FAIL  shop             .nearhostrc: line 3: key \"extra\" is not understood by this version of Nearhost; update Nearhost, or remove it\n" +
		"      fix: Correct .nearhostrc.\n" +
		"ok    blog             no problems found\n" +
		"\n" +
		"1 check failed.\n"
	if out != want {
		t.Fatalf("doctor printed:\n%s\nwant:\n%s", out, want)
	}
	if strings.Join(opened, " ") != config.LocalServer {
		t.Errorf("opened the files of %v, want this machine's once", opened)
	}

	// Without shop, nothing fails.
	cfg := loadSaved(t, path)
	cfg.Projects = cfg.Projects[1:]
	saveConfig(t, path, cfg)
	f.hosts = "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 blog.test\n127.0.0.1 old.test\n# nearhost:end\n"
	out, err = execute(t, newDoctorCmd(withProjects(f.backend(), nil, &opened)), "doctor")
	if err != nil || !strings.HasSuffix(out, "\nProjects\nok    blog             no problems found\n\nAll checks passed.\n") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// A name missing from the hosts block fails the machine check, which names
// it, so its projects don't report it again with another fix.
func TestDoctorReportsAMissingHostsNameOnce(t *testing.T) {
	path := configHome(t)
	blog := localProject("blog", 8000, true)
	blog.Path = t.TempDir()
	saveConfig(t, path, &config.Config{Projects: []config.Project{blog}})
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n"
	f.lookup = func(_ context.Context, name string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	var opened []string
	out, err := execute(t, newDoctorCmd(withProjects(f.backend(), nil, &opened)), "doctor")
	if err == nil || err.Error() != "1 check failed" || !strings.Contains(out, "FAIL  hosts block      missing blog.test\n") ||
		!strings.HasSuffix(out, "\nProjects\nok    blog             no problems found\n\n1 check failed.\n") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	out, err = execute(t, newDoctorCmd(withProjects(f.backend(), nil, &opened)), "doctor", "--json")
	if strings.Contains(out, "hosts.unresolved") {
		t.Fatalf("--json: %v\n%s", err, out)
	}
}

func TestDoctorPrintsEachFindingWithItsLevelFixAndSnippet(t *testing.T) {
	var out bytes.Buffer
	printProjects(&out, []projectReport{
		{name: "shop", findings: []doctor.Finding{
			{ID: "advice.x", Severity: doctor.SeverityWarning, Message: "port 5173 clashes",
				Fix: doctor.Fix{Kind: doctor.FixProject, Text: "Change the project's settings as shown.", Snippet: "VITE_PORT=5174\nAPP_PORT=8001"}},
			{ID: "routes.not-listening", Severity: doctor.SeverityInfo, Message: "nothing listens on port 3000",
				Fix: doctor.Fix{Kind: doctor.FixManual, Text: "Start it."}},
		}},
		{name: "blog", findings: []doctor.Finding{}},
	})
	want := "" +
		"\n" +
		"Projects\n" +
		"warn  shop             port 5173 clashes\n" +
		"      fix: Change the project's settings as shown.\n" +
		"          VITE_PORT=5174\n" +
		"          APP_PORT=8001\n" +
		"info  shop             nothing listens on port 3000\n" +
		"      fix: Start it.\n" +
		"ok    blog             no problems found\n"
	if out.String() != want {
		t.Fatalf("printed:\n%s\nwant:\n%s", out.String(), want)
	}

	// A section with no project prints nothing.
	out.Reset()
	printProjects(&out, nil)
	if out.Len() != 0 {
		t.Errorf("printed %q for no projects", out.String())
	}
}

// runSplit runs doctor with b, keeping standard output and standard error
// apart.
func runSplit(t *testing.T, b doctorBackend, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd()
	root.AddCommand(newDoctorCmd(b))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}

type jsonDoctor struct {
	Checks []struct {
		Name   string `json:"name"`
		Level  string `json:"level"`
		Detail string `json:"detail"`
		Fix    string `json:"fix"`
	} `json:"checks"`
	Findings []doctor.Finding `json:"findings"`
}

// decodeOne decodes stdout as one JSON object and nothing else.
func decodeOne(t *testing.T, stdout string) jsonDoctor {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	var got jsonDoctor
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("stdout is not the JSON: %v\n%s", err, stdout)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("more than one JSON object on stdout:\n%s", stdout)
	}
	return got
}

func TestDoctorJSON(t *testing.T) {
	shopAndBlog(t)
	f := healthy()
	f.hosts = shopAndBlogHosts
	f.trust.nss = proxy.NSSStatus{State: proxy.NSSMissing}
	f.portErr = map[string]error{"tcp 80": syscall.EADDRINUSE}
	var opened []string
	stdout, stderr, err := runSplit(t, withProjects(f.backend(), nil, &opened), "doctor", "--json")
	if err == nil || err.Error() != "2 checks failed" {
		t.Fatalf("err = %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr: %q", stderr)
	}
	got := decodeOne(t, stdout)
	levels := map[string]string{}
	for _, c := range got.Checks {
		levels[c.Name] = c.Level
	}
	if levels["port 443"] != "ok" || levels["browser stores"] != "warn" || levels["port 80"] != "fail" || levels["port 6327"] != "ok" || len(levels) != 7 {
		t.Errorf("check levels %v", levels)
	}
	for _, c := range got.Checks {
		if c.Name == "port 80" && (c.Detail != "address already in use" || !strings.Contains(c.Fix, "--http-port")) {
			t.Errorf("port 80 check %+v", c)
		}
	}
	want := []doctor.Finding{{
		ID: "nearhostrc.invalid", Severity: doctor.SeverityError, Project: "shop",
		Message: `.nearhostrc: line 3: key "extra" is not understood by this version of Nearhost; update Nearhost, or remove it`,
		File:    ".nearhostrc", Line: 3,
		Fix: doctor.Fix{Kind: doctor.FixProject, Text: "Correct .nearhostrc."},
	}}
	if !reflect.DeepEqual(got.Findings, want) {
		t.Errorf("findings %+v\nwant %+v", got.Findings, want)
	}

	// Nothing failed: the exit status is zero, and findings is an array even
	// when it is empty.
	f.portErr, f.hosts = nil, "127.0.0.1 localhost\n"
	f.trust.nss = proxy.NSSStatus{State: proxy.NSSCovered}
	cfgPath := configHome(t)
	saveConfig(t, cfgPath, &config.Config{})
	stdout, _, err = runSplit(t, withProjects(f.backend(), nil, &opened), "doctor", "--json")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `"findings": []`) {
		t.Errorf("findings is not an empty array:\n%s", stdout)
	}
	decodeOne(t, stdout)
}

func TestDoctorReportsProjectsOfAnUnreachableServerUnchecked(t *testing.T) {
	path := configHome(t)
	shop, cart, idle := remoteProject("shop"), remoteProject("cart"), remoteProject("idle")
	shop.Server, cart.Server, idle.Server = "box", "box", "box"
	idle.Enabled = false
	saveConfig(t, path, &config.Config{
		Servers:  []config.Server{{Name: "box", SSHHost: "box.lan"}},
		Projects: []config.Project{shop, cart, idle},
	})
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 cart.test\n127.0.0.1 idle.test\n127.0.0.1 shop.test\n# nearhost:end\n"
	f.serverErr = map[string]error{"box": errors.New("no route to host")}
	var opened []string
	out, err := execute(t, newDoctorCmd(withProjects(f.backend(), nil, &opened)), "doctor")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	const fix = "      fix: Make ssh to the server work (see the server check above), then run `nearhost doctor` again.\n"
	wantContains(t, out, "\nProjects\n"+
		"info  shop             not checked: server box can't be reached\n"+fix+
		"info  cart             not checked: server box can't be reached\n"+fix+
		"\n1 check failed.\n")
	if strings.Contains(out, "idle  ") || len(opened) != 0 {
		t.Errorf("opened %v\n%s", opened, out)
	}

	stdout, _, _ := runSplit(t, withProjects(f.backend(), nil, &opened), "doctor", "--json")
	got := decodeOne(t, stdout)
	want := doctor.Finding{ID: "projects.unchecked", Severity: doctor.SeverityInfo, Project: "shop",
		Message: "not checked: server box can't be reached",
		Fix:     doctor.Fix{Kind: doctor.FixManual, Text: "Make ssh to the server work (see the server check above), then run `nearhost doctor` again."}}
	if len(got.Findings) != 2 || !reflect.DeepEqual(got.Findings[0], want) || got.Findings[1].Project != "cart" {
		t.Errorf("findings %+v", got.Findings)
	}

	// ssh works, but the files can't be opened: the error says why, and the
	// server is tried once for both projects.
	f.serverErr = nil
	opened = nil
	out, err = execute(t, newDoctorCmd(withProjects(f.backend(), nil, &opened)), "doctor")
	if err != nil {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out,
		"info  shop             not checked: server box can't be reached: connect to box.lan: no route to host\n"+fix,
		"info  cart             not checked: server box can't be reached: connect to box.lan: no route to host\n"+fix)
	if strings.Join(opened, " ") != "box" {
		t.Errorf("opened %v, want box once", opened)
	}
}

func TestDoctorOpensEachServersFilesOnceAndClosesThem(t *testing.T) {
	path := configHome(t)
	shop, cart, gone := remoteProject("shop"), remoteProject("cart"), remoteProject("gone")
	saveConfig(t, path, &config.Config{Servers: mini, Projects: []config.Project{shop, cart, gone}})
	box := &memFiles{dirs: map[string]map[string]string{
		shop.Path: {".nearhostrc": unknownKeyRc},
		cart.Path: {},
	}}
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 cart.test\n127.0.0.1 gone.test\n127.0.0.1 shop.test\n# nearhost:end\n"
	var opened []string
	out, err := execute(t, newDoctorCmd(withProjects(f.backend(), map[string]serverFiles{"mini": box}, &opened)), "doctor")
	if err == nil || err.Error() != "1 check failed" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	wantContains(t, out,
		"FAIL  shop             .nearhostrc: line 3:",
		"ok    cart             no problems found\n",
		"info  gone             not checked: /srv/gone: no such directory\n"+
			"      fix: Check that the project's directory exists on server mini, or correct its path in Nearhost.\n")
	if strings.Join(opened, " ") != "mini" || box.closed != 1 {
		t.Errorf("opened %v, closed %d times", opened, box.closed)
	}
}

func TestDoctorGivesUpOnAProjectWhoseChecksHang(t *testing.T) {
	defer func(d time.Duration) { projectTimeout = d }(projectTimeout)
	projectTimeout = 50 * time.Millisecond
	path := configHome(t)
	shop, cart := remoteProject("shop"), remoteProject("cart")
	saveConfig(t, path, &config.Config{Servers: mini, Projects: []config.Project{shop, cart}})
	box := &memFiles{dirs: map[string]map[string]string{shop.Path: {}, cart.Path: {}}, block: make(chan struct{})}
	defer close(box.block)
	f := healthy()
	f.hosts = "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 cart.test\n127.0.0.1 shop.test\n# nearhost:end\n"
	var opened []string
	done := make(chan string)
	go func() {
		out, _ := execute(t, newDoctorCmd(withProjects(f.backend(), map[string]serverFiles{"mini": box}, &opened)), "doctor")
		done <- out
	}()
	select {
	case out := <-done:
		const fix = "      fix: Check the connection to server mini, then run `nearhost doctor` again.\n"
		wantContains(t, out,
			"info  shop             not checked: the checks did not finish in time\n"+fix,
			"info  cart             not checked: the checks did not finish in time\n"+fix)
	case <-time.After(5 * time.Second):
		t.Fatal("doctor hung on a server that does not answer")
	}
}

func TestDraftProjectForADirectoryNotAdded(t *testing.T) {
	cfgPath := configHome(t)
	cfg := &config.Config{Projects: []config.Project{localProject("web", 8001, true)}}
	saveConfig(t, cfgPath, cfg)
	dir := filepath.Join(t.TempDir(), "My Shop")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{
		".env":        "APP_PORT=8000\n",
		".nearhostrc": "version: 1\nroutes:\n  - port: ${APP_PORT}\n    hosts: [api.local.x.io]\n",
	})
	local := config.Server{Name: config.LocalServer}
	reader := localFiles{}.reader(dir)
	p := draftProject(cfg, cfgPath, local, dir, reader)

	added, err := newProject(cfg, cfgPath, local, addFlags{}, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "My Shop" || p.Alias != added.Alias || p.Alias != "my-shop.test" || p.Server != config.LocalServer || p.Path != dir {
		t.Errorf("draft %+v", p)
	}
	var web *config.Route
	for i := range p.Routes {
		if p.Routes[i].Role == config.RoleWeb {
			web = &p.Routes[i]
		}
	}
	if web == nil || web.Port != 8000 || !reflect.DeepEqual(web.Hosts, []string{"api.local.x.io"}) {
		t.Errorf("draft routes %+v", p.Routes)
	}

	// A .nearhostrc that doesn't parse leaves what discovery found.
	writeFiles(t, dir, map[string]string{".nearhostrc": "version: [\n"})
	res, _, err := discoverOnce(cfg, cfgPath, reader, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p = draftProject(cfg, cfgPath, local, dir, reader)
	if len(p.Routes) == 0 || !reflect.DeepEqual(p.Routes, discoveredRoutes(res)) || p.Alias != "my-shop.test" {
		t.Errorf("draft with a broken file %+v, want the routes %+v", p, discoveredRoutes(res))
	}
}

// projectFindings reads the hosts block only when Nearhost manages the hosts
// file, so a name missing from it is reported only then.
func TestProjectFindingsReadsTheHostsBlock(t *testing.T) {
	cfgPath := configHome(t)
	shop := localProject("shop", 3000, true)
	shop.Path = t.TempDir()
	cfg := &config.Config{Projects: []config.Project{shop}}
	saveConfig(t, cfgPath, cfg)
	nx := func(_ context.Context, name string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	ids := func(env doctorEnv) []string {
		var out []string
		for _, f := range projectFindings(context.Background(), env, cfg, cfgPath, shop, true, false, localFiles{}.reader(shop.Path)) {
			out = append(out, f.ID)
		}
		return out
	}
	hostsFile := func(content string, err error) func() (string, error) {
		return func() (string, error) { return content, err }
	}
	for _, tc := range []struct {
		name string
		env  doctorEnv
		want []string
	}{
		{"in the block", doctorEnv{lookup: nx, readHosts: hostsFile("# nearhost:start\n127.0.0.1 shop.test\n# nearhost:end\n", nil)}, nil},
		{"missing from the block", doctorEnv{lookup: nx, readHosts: hostsFile("127.0.0.1 localhost\n", nil)}, []string{"hosts.unresolved"}},
		{"no hosts file yet", doctorEnv{lookup: nx, readHosts: hostsFile("", fs.ErrNotExist)}, []string{"hosts.unresolved"}},
		{"hosts not managed", doctorEnv{lookup: nx, noHosts: true, readHosts: hostsFile("127.0.0.1 localhost\n", nil)}, nil},
		{"hosts file unreadable", doctorEnv{lookup: nx, readHosts: hostsFile("", fs.ErrPermission)}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ids(tc.env); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSystemDoctorEnvPointsAtTheRootCertificate(t *testing.T) {
	storage := t.TempDir()
	if env := systemDoctorEnv(nil, true, storage); env.caCert != proxy.RootCertPath(storage) || !env.noHosts || env.goos == "" {
		t.Errorf("env %+v", env)
	}
	if env := systemDoctorEnv(nil, false, ""); env.caCert != "" {
		t.Errorf("caCert %q without storage", env.caCert)
	}
}

// The CA check counts a NEARHOST_CA_CERT set in the shell, as its fix
// suggests. Only a project on this machine runs Compose in Nearhost's own
// environment, so only it gets that environment.
func TestDoctorInputPassesTheEnvironmentToLocalProjects(t *testing.T) {
	const key = "NEARHOST_TEST_DOCTOR_ENVIRON"
	t.Setenv(key, "from the shell")
	cfgPath := configHome(t)
	local, remote := localProject("shop", 3000, true), remoteProject("api")
	local.Path = t.TempDir()
	cfg := &config.Config{Servers: mini, Projects: []config.Project{local, remote}}
	saveConfig(t, cfgPath, cfg)
	env := systemDoctorEnv(nil, true, "")

	in := doctorInput(env, cfg, cfgPath, local, true, false, localFiles{}.reader(local.Path))
	if got := in.Environ[key]; got != "from the shell" {
		t.Errorf("local project: Environ[%s] = %q", key, got)
	}
	in = doctorInput(env, cfg, cfgPath, remote, true, false, localFiles{}.reader(t.TempDir()))
	if in.Environ != nil {
		t.Errorf("remote project: Environ = %v, want nil", in.Environ)
	}
}

func TestProjectFindingsCountsTheCACertInTheShell(t *testing.T) {
	cfgPath := configHome(t)
	shop := localProject("shop", 3000, true)
	shop.Path = t.TempDir()
	compose := "services:\n  worker:\n    environment:\n      CALLBACK: https://shop.test/hook\n" +
		"    volumes:\n      - \"${NEARHOST_CA_CERT:?set it}:/c:ro\"\n"
	if err := os.WriteFile(filepath.Join(shop.Path, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Projects: []config.Project{shop}}
	saveConfig(t, cfgPath, cfg)
	const ca = "/ca/root.crt"
	hasCA := func(environ []string) bool {
		env := doctorEnv{caCert: ca, goos: "linux", noHosts: true, environ: func() []string { return environ }}
		for _, f := range projectFindings(context.Background(), env, cfg, cfgPath, shop, true, false, localFiles{}.reader(shop.Path)) {
			if f.ID == "docker.ca" {
				return true
			}
		}
		return false
	}
	if !hasCA(nil) {
		t.Error("docker.ca not reported with NEARHOST_CA_CERT unset")
	}
	if hasCA([]string{"NEARHOST_CA_CERT=" + ca}) {
		t.Error("docker.ca reported with NEARHOST_CA_CERT set in the shell")
	}
}

// The hosts block check can fail without naming a project's missing host:
// for stale names only, or a hosts file it could not read. Then the
// project's own finding is all that names it.
func TestDoctorKeepsAMissingNameTheHostsCheckDoesNotName(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first string // the hosts file the machine check reads
		err   error
		check string
	}{
		{"stale names only", "127.0.0.1 localhost\n# nearhost:start\n127.0.0.1 blog.test\n127.0.0.1 gone.test\n# nearhost:end\n", nil,
			"FAIL  hosts block      no longer configured: gone.test\n"},
		{"unreadable", "", fs.ErrPermission, "FAIL  hosts block      permission denied\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := configHome(t)
			blog := localProject("blog", 8000, true)
			blog.Path = t.TempDir()
			saveConfig(t, path, &config.Config{Projects: []config.Project{blog}})
			f := healthy()
			f.lookup = func(_ context.Context, name string) ([]netip.Addr, error) {
				return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
			}
			var opened []string
			b := withProjects(f.backend(), nil, &opened)
			// The machine check reads the file first; by the time the
			// project is checked, blog.test has gone from it.
			reads := 0
			b.env.readHosts = func() (string, error) {
				reads++
				if reads == 1 {
					return tc.first, tc.err
				}
				return "127.0.0.1 localhost\n", nil
			}
			out, _ := execute(t, newDoctorCmd(b), "doctor")
			if !strings.Contains(out, tc.check) || !strings.Contains(out, "FAIL  blog             blog.test does not resolve to this machine") {
				t.Fatalf("doctor printed:\n%s", out)
			}
		})
	}
}

// Only the names the hosts block check reports as missing are left out of
// the projects' findings, whichever project they belong to.
func TestWithoutReportedDropsOnlyTheNamesTheHostsCheckNames(t *testing.T) {
	unresolved := func(host string) doctor.Finding {
		return doctor.Finding{ID: "hosts.unresolved", Severity: doctor.SeverityError, Host: host,
			Message: host + " does not resolve to this machine, and is not in the hosts file"}
	}
	other := doctor.Finding{ID: "hosts.public", Severity: doctor.SeverityWarning, Host: "app.test", Message: "app.test is a real name"}
	projects := []projectReport{
		{name: "app", findings: []doctor.Finding{unresolved("app.test"), unresolved("admin.app.test"), other}},
		{name: "shop", findings: []doctor.Finding{unresolved("shop.test")}},
	}
	got := withoutReported(projects, []string{"app.test", "shop.test"})
	want := []projectReport{
		{name: "app", findings: []doctor.Finding{unresolved("admin.app.test"), other}},
		{name: "shop", findings: []doctor.Finding{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if len(projects[0].findings) != 3 {
		t.Errorf("changed the projects passed in: %+v", projects)
	}
	// Names compare as DNS does, whatever their case.
	if got := withoutReported(projects[1:], []string{"Shop.TEST"}); len(got[0].findings) != 0 {
		t.Errorf("kept a name the check lists in another case: %+v", got)
	}
}

func TestDraftProjectNearhostrcBeatsInferredHosts(t *testing.T) {
	cfgPath := configHome(t)
	cfg := &config.Config{}
	saveConfig(t, cfgPath, cfg)
	enableHostedPreset(t, cfgPath)
	cfg = loadSaved(t, cfgPath)
	dir := filepath.Join(t.TempDir(), "shop")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{".env": hostedEnv})
	local := config.Server{Name: config.LocalServer}
	// Without the file, the preset infers a host for the route.
	if hosts := draftProject(cfg, cfgPath, local, dir, localFiles{}.reader(dir)).AllHosts(); !slices.Contains(hosts, "app.local.x.io") {
		t.Fatalf("nothing inferred to override: %v", hosts)
	}
	writeFiles(t, dir, map[string]string{".nearhostrc": "version: 1\nroutes:\n  - port: ${APP_PORT}\n    hosts: [auth.local.x.io]\n"})
	p := draftProject(cfg, cfgPath, local, dir, localFiles{}.reader(dir))
	found := false
	for _, r := range p.Routes {
		if r.Port == 8220 {
			found = true
			if !reflect.DeepEqual(r.Hosts, []string{"auth.local.x.io"}) {
				t.Errorf("route 8220 hosts = %v", r.Hosts)
			}
		}
	}
	if !found {
		t.Errorf("no route on 8220: %+v", p.Routes)
	}
}

func TestDraftProjectDropsTheAliasFromSecondaryRoutes(t *testing.T) {
	cfgPath := configHome(t)
	saveConfig(t, cfgPath, &config.Config{})
	enableReverbPreset(t, cfgPath)
	cfg := loadSaved(t, cfgPath)
	dir := filepath.Join(t.TempDir(), "rapp")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, map[string]string{".env": reverbEnv})
	local := config.Server{Name: config.LocalServer}
	p := draftProject(cfg, cfgPath, local, dir, localFiles{}.reader(dir))
	wantAliasHostDropped(t, p.Routes)
}
