// Package testutil holds helpers shared by tests. Only _test.go files import
// it.
package testutil

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// FreePort returns a loopback TCP port that was unused a moment ago. The
// system picks it, so any process may be given the same one next: use it for
// what is bound at once or may fail to bind, and ProxyPort for a proxy.
func FreePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// WaitUntil polls cond until it holds and fails the test after timeout.
func WaitUntil(t testing.TB, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// HTTPSClient returns a client that trusts only root and reaches alias by
// dialling 127.0.0.1:port, so no hosts entry is needed. Connections are not
// kept alive: each request sees the proxy's current configuration.
func HTTPSClient(t testing.TB, root *x509.Certificate, alias string, port int) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(root)
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, ServerName: alias},
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", port))
		},
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second}
}

// Proxy ports come from a block of proxyPortBlock ports that the test process
// picks by its pid, inside 20000-29999. That range is below the ports the
// system hands out by itself (Linux 32768-60999, macOS 49152-65535), so
// neither an outgoing connection nor a ":0" listener of any process can take
// a port between the moment it is chosen here and the moment the proxy binds
// it. Test binaries that run side by side share a block only when their pids
// are a multiple of proxyPortBlocks apart, and then still clash only if both
// are handed the same port within the moment before one of them binds it:
// ProxyPort hands out no port that is bound.
const (
	proxyPortFirst  = 20000
	proxyPortBlock  = 100
	proxyPortBlocks = 100
)

// proxyPortNext counts the ports handed out; it steps through the block.
var proxyPortNext atomic.Uint32

func proxyPortBase() int {
	return proxyPortFirst + (os.Getpid()%proxyPortBlocks)*proxyPortBlock
}

// ProxyPort returns a port for a proxy listener: free for both TCP and UDP
// on every interface (the proxy also listens for HTTP/3 on the UDP port
// matching its HTTPS port) and not handed out before by this process, until
// its block of proxyPortBlock ports has been used up and starts over.
func ProxyPort(t testing.TB) int {
	t.Helper()
	base := proxyPortBase()
	for i := 0; i < proxyPortBlock; i++ {
		port := base + int(proxyPortNext.Add(1)-1)%proxyPortBlock
		if bindable(port) {
			return port
		}
	}
	t.Fatalf("no port free for both TCP and UDP in %d-%d", base, base+proxyPortBlock-1)
	return 0
}

// bindable reports whether port can be bound right now on TCP and on UDP.
func bindable(port int) bool {
	addr := fmt.Sprintf(":%d", port)
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	l.Close()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return false
	}
	pc.Close()
	return true
}

// ProxyPorts returns two different ports for a proxy's HTTPS and HTTP
// listeners.
func ProxyPorts(t testing.TB) (httpsPort, httpPort int) {
	t.Helper()
	httpsPort = ProxyPort(t)
	// Only a block with a single usable port left could repeat one.
	for i := 0; i < 3; i++ {
		if httpPort = ProxyPort(t); httpPort != httpsPort {
			return httpsPort, httpPort
		}
	}
	t.Fatal("no second free port for the proxy")
	return 0, 0
}

// TempDir is t.TempDir with a short path (ssh control sockets have a length
// limit, and pgrep can take the path as a pattern) and a patient removal:
// Caddy issues certificates in the background and can still be writing one
// into its storage for a moment after it has stopped, which makes a single
// RemoveAll fail.
func TempDir(t testing.TB, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		WaitUntil(t, 10*time.Second, func() bool { return os.RemoveAll(dir) == nil }, "removal of "+dir)
	})
	return dir
}

// FakeSSH returns the path of the fake OpenSSH client, the program in
// internal/testutil/fakessh, built on the first call in this test process.
// It records its arguments to $FAKE_SSH_LOG and is steered by the FAKE_SSH_*
// variables described in that program.
func FakeSSH(t testing.TB) string {
	t.Helper()
	return build(t, "github.com/nearhost/nearhost/internal/testutil/fakessh", "ssh")
}

// FakeTool puts a copy of the fake command-line tool, the program in
// internal/testutil/faketool, in dir as name (name.exe on Windows), built on
// the first call in this test process, and writes does to dir/name.does for
// it to read. does is "exit N", to exit with code N, or "copy PATH", to copy
// standard input to PATH and exit 0. The program is copied, not linked, so
// looking name up on PATH finds it on every system.
func FakeTool(t testing.TB, dir, name, does string) {
	t.Helper()
	src := build(t, "github.com/nearhost/nearhost/internal/testutil/faketool", "faketool")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, name)
	if runtime.GOOS == "windows" {
		dst += ".exe"
	}
	if err := os.WriteFile(dst, b, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".does"), []byte(does), 0o644); err != nil {
		t.Fatal(err)
	}
}

// SFTPServer finds the OpenSSH SFTP server binary, which speaks the protocol
// on its standard streams and so can stand in for `ssh -s host sftp`, and
// skips the test when there is none.
func SFTPServer(t testing.TB) string {
	t.Helper()
	for _, p := range []string{"/usr/libexec/sftp-server", "/usr/lib/openssh/sftp-server", "/usr/libexec/openssh/sftp-server", "/usr/lib/ssh/sftp-server", `C:\Windows\System32\OpenSSH\sftp-server.exe`} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("no sftp-server binary on this machine")
	return ""
}

// A built is a test program built once per test process: its path, or the
// reason it could not be built.
type built struct {
	once sync.Once
	path string
	err  error
}

var (
	// builtMu guards builds.
	builtMu sync.Mutex
	// builds holds the test programs by package path.
	builds = map[string]*built{}
)

// userDirNames are the variables that place the user's home, configuration
// and caches. The go command finds its settings and build caches through them
// (on Windows through LOCALAPPDATA and USERPROFILE).
var userDirNames = []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "LOCALAPPDATA", "USERPROFILE"}

// userDirs holds the userDirNames variables as they were when the package was
// initialised. Some TestMain functions move them before the first build, so
// the build is handed these.
var userDirs = func() map[string]string {
	dirs := map[string]string{}
	for _, k := range userDirNames {
		if v, ok := os.LookupEnv(k); ok {
			dirs[k] = v
		}
	}
	return dirs
}()

// buildEnv returns this process's environment with the variables in
// userDirs as they were when the package was initialised.
func buildEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(userDirNames, k) {
			env = append(env, kv)
		}
	}
	for k, v := range userDirs {
		env = append(env, k+"="+v)
	}
	return env
}

// build returns the path of the program pkg built as name (name.exe on
// Windows). The first call for pkg in this test process builds it into a
// directory of its own, so test processes running side by side never share
// a file; later calls, from any goroutine, return the same path. The
// directory is left to the system's temporary file cleaning, as no test owns
// it. A failed build fails this and every later call. The go command is the
// one on PATH, which go test puts first.
func build(t testing.TB, pkg, name string) string {
	t.Helper()
	path, err := buildOnce(pkg, name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// buildOnce is build without the test: it returns the reason the program
// could not be built rather than failing the test, so any goroutine may call
// it.
func buildOnce(pkg, name string) (string, error) {
	builtMu.Lock()
	b, ok := builds[pkg]
	if !ok {
		b = &built{}
		builds[pkg] = b
	}
	builtMu.Unlock()
	b.once.Do(func() {
		goTool, err := exec.LookPath("go")
		if err != nil {
			b.err = fmt.Errorf("build %s: the go command is not on PATH: %w", pkg, err)
			return
		}
		dir, err := os.MkdirTemp("", "nearhost-"+filepath.Base(pkg)+"-*")
		if err != nil {
			b.err = fmt.Errorf("build %s: %w", pkg, err)
			return
		}
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(dir, name)
		cmd := exec.Command(goTool, "build", "-o", path, pkg)
		// Run in testutil's own directory, which is inside the module, so
		// the build works after a test has changed to a directory outside it.
		_, self, _, ok := runtime.Caller(0)
		if !ok {
			b.err = fmt.Errorf("build %s: cannot find the source directory", pkg)
			return
		}
		cmd.Dir = filepath.Dir(self)
		cmd.Env = buildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			b.err = fmt.Errorf("build %s: %v\n%s", pkg, err, out)
			return
		}
		b.path = path
	})
	return b.path, b.err
}

// Upstream starts a local HTTP server answering "<name>:<host>" and returns
// its port.
func Upstream(t testing.TB, name string) int {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, name+":"+r.Host)
	}))
	t.Cleanup(up.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(up.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
