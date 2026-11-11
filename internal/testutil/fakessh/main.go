// Command fakessh is a fake OpenSSH client for nearhost tests. It records its
// arguments to $FAKE_SSH_LOG and simulates the flags nearhost uses; the
// FAKE_SSH_* variables steer it, each read only when it is set. Tests build
// it with testutil.FakeSSH.
//
// A master (-N -M -S sock host) creates its socket and stays up until the
// socket is removed, `-O exit` asks it to, or it gets TERM or INT. It polls
// every 0.1 s, as the shell script it replaces did.
//
// A plain process (-N with neither -M nor -S) acts as a per-process ssh: it
// listens on each -L, reports each -R, and stays up until it is killed or a
// file named for its pid appears in $FAKE_SSH_DROP_DIR. `-G host` prints the
// configuration and exits before anything else. A -R whose local half is a
// Windows path (C:\...) is logged, then rejected before any connection, as
// OpenSSH, which splits the spec at its colons, rejects it.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

// run does what ssh with args would do in the test and returns the exit
// status. A master that is signalled exits from its signal handler, and an
// SFTP subsystem request may replace this process with the SFTP server.
func run(args []string) int {
	parentGone := watchParent()
	all := strings.Join(args, " ")
	if hasArg(args, "-G") {
		return printConfig(args[len(args)-1])
	}
	appendLine(os.Getenv("FAKE_SSH_LOG"), all)
	// $FAKE_SSH_PGID_LOG gets one line per invocation: this process's group,
	// then argv.
	if p := os.Getenv("FAKE_SSH_PGID_LOG"); p != "" {
		appendLine(p, fmt.Sprintf("%d %s", pgid(), all))
	}
	if spec := windowsRForward(args); spec != "" {
		fmt.Fprintf(os.Stderr, "Bad remote forwarding specification '%s'\n", spec)
		return 255
	}

	sock, mode, larg, rarg, last, master := "", "", "", "", "", false
	prev := ""
	var lforwards, rforwards []string
	plain := hasArg(args, "-N") && !hasArg(args, "-M") && !hasArg(args, "-S")
	for _, a := range args {
		switch prev {
		case "-S":
			sock = a
		case "-O":
			mode = a
		case "-L":
			larg = a
			lforwards = append(lforwards, a)
		case "-R":
			rarg = a
			rforwards = append(rforwards, a)
		}
		if a == "-M" {
			master = true
		}
		prev = a
		last = a
	}

	// While the file named by $FAKE_SSH_MASTER_DOWN exists, every master
	// fails with a refused connection.
	if f := os.Getenv("FAKE_SSH_MASTER_DOWN"); f != "" && (master || plain) && exists(f) {
		fmt.Fprintf(os.Stderr, "ssh: connect to host %s port 22: Connection refused\n", last)
		return 255
	}
	// A master for $FAKE_SSH_AUTH_FAIL_HOST waits while the file named by
	// $FAKE_SSH_AUTH_HOLD exists. Then that host, and every host while the
	// file named by $FAKE_SSH_AUTH_FAIL_FILE exists, fails authentication.
	authFail := os.Getenv("FAKE_SSH_AUTH_FAIL") != ""
	if host := os.Getenv("FAKE_SSH_AUTH_FAIL_HOST"); host != "" && last == host {
		if hold := os.Getenv("FAKE_SSH_AUTH_HOLD"); hold != "" && (master || plain) {
			for exists(hold) {
				time.Sleep(50 * time.Millisecond)
			}
		}
		authFail = true
	}
	if f := os.Getenv("FAKE_SSH_AUTH_FAIL_FILE"); f != "" && exists(f) {
		authFail = true
	}
	// $FAKE_SSH_CHECK_REFUSE is the message of a server that refuses to run
	// the connectivity check's command.
	if msg := os.Getenv("FAKE_SSH_CHECK_REFUSE"); msg != "" && last == "true" {
		fmt.Fprintln(os.Stderr, msg)
		return 1
	}
	// $FAKE_SSH_SFTP_SERVER is an sftp-server that serves the SFTP subsystem,
	// from the directory $FAKE_SSH_SFTP_HOME when that is set.
	if server := os.Getenv("FAKE_SSH_SFTP_SERVER"); server != "" && last == "sftp" {
		argv := []string{server, "-R"}
		if home := os.Getenv("FAKE_SSH_SFTP_HOME"); home != "" {
			argv = append(argv, "-d", home)
		}
		return execServer(argv)
	}

	switch mode {
	case "check":
		if exists(sock) {
			return 0
		}
		fmt.Fprintf(os.Stderr, "Control socket connect(%s): No such file or directory\n", sock)
		return 255
	case "forward":
		return forward(sock, larg, rarg)
	case "cancel":
		// Wait for as long as the file named by $FAKE_SSH_CANCEL_WAIT exists.
		if f := os.Getenv("FAKE_SSH_CANCEL_WAIT"); f != "" {
			for exists(f) {
				time.Sleep(50 * time.Millisecond)
			}
		}
		// Fail while the file named by $FAKE_SSH_CANCEL_FAIL exists.
		if f := os.Getenv("FAKE_SSH_CANCEL_FAIL"); f != "" && exists(f) {
			fmt.Fprintln(os.Stderr, "mux_client_forward: forwarding request failed")
			return 255
		}
		return 0
	case "exit":
		os.Remove(sock)
		touch(sock + ".exit")
		return 0
	}

	if authFail {
		fmt.Fprintln(os.Stderr, "user@host: Permission denied (publickey).")
		return 255
	}
	if os.Getenv("FAKE_SSH_HOSTKEY_FAIL") != "" {
		fmt.Fprintln(os.Stderr, "Host key verification failed.")
		return 255
	}

	if plain {
		return runPlain(last, lforwards, rforwards, hasArg(args, "ExitOnForwardFailure=yes"), parentGone)
	}

	// BatchMode connectivity check: ssh ... host true
	if last == "true" {
		return 0
	}

	// $FAKE_SSH_ALTERNATE makes master launches alternate between an
	// authentication failure and a plain connection failure, starting with the
	// former.
	if os.Getenv("FAKE_SSH_ALTERNATE") != "" && last != "sftp" {
		if exists(sock + ".odd") {
			os.Remove(sock + ".odd")
			fmt.Fprintln(os.Stderr, "ssh: connect to host mini port 22: Connection refused")
			return 255
		}
		touch(sock + ".odd")
		fmt.Fprintln(os.Stderr, "user@host: Permission denied (publickey).")
		return 255
	}

	// Without $FAKE_SSH_SFTP_SERVER the SFTP subsystem is not simulated.
	if last == "sftp" {
		fmt.Fprintln(os.Stderr, "fake ssh: sftp not supported")
		return 1
	}

	// $FAKE_SSH_HANG makes the master stall without ever creating its socket;
	// it leaves when asked to exit (-O exit drops $sock.exit) or when killed.
	if os.Getenv("FAKE_SSH_HANG") != "" {
		os.Remove(sock + ".exit")
		for !exists(sock + ".exit") {
			if parentGone() {
				return 255
			}
			time.Sleep(100 * time.Millisecond)
		}
		return 0
	}

	// $FAKE_SSH_FLAKY_ONCE=<seconds> makes the first master die after that
	// long.
	if s := os.Getenv("FAKE_SSH_FLAKY_ONCE"); s != "" && !exists(sock+".flaked") {
		touch(sock + ".flaked")
		touch(sock)
		secs, _ := strconv.ParseFloat(s, 64)
		deadline := time.Now().Add(time.Duration(secs * float64(time.Second)))
		for time.Now().Before(deadline) {
			if parentGone() {
				return 255
			}
			time.Sleep(min(50*time.Millisecond, time.Until(deadline)))
		}
		os.Remove(sock)
		fmt.Fprintln(os.Stderr, "client_loop: send disconnect: Broken pipe")
		return 255
	}

	// Master mode: create the socket and stay up until it is removed. TERM
	// and INT remove it and end the master cleanly.
	touch(sock)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-signals
		os.Remove(sock)
		os.Exit(0)
	}()
	for exists(sock) {
		if parentGone() {
			return 255
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0
}

// hasArg reports whether args holds want as an argument.
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// windowsPath matches a path that starts with a Windows drive.
var windowsPath = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// windowsRForward returns the first -R in args whose local half is a
// Windows path, or "".
func windowsRForward(args []string) string {
	for i, a := range args[:max(0, len(args)-1)] {
		if a != "-R" {
			continue
		}
		if _, local, _ := strings.Cut(args[i+1], ":"); windowsPath.MatchString(local) {
			return args[i+1]
		}
	}
	return ""
}

// printConfig answers `ssh -G host`: the host name, then the lines of
// $FAKE_SSH_G_CONFIG. It makes no connection. $FAKE_SSH_G_FAIL makes it
// fail instead, with that as its message.
func printConfig(host string) int {
	if msg := os.Getenv("FAKE_SSH_G_FAIL"); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 255
	}
	fmt.Printf("hostname %s\n", host)
	if cfg := os.Getenv("FAKE_SSH_G_CONFIG"); cfg != "" {
		for _, line := range strings.Split(cfg, "\n") {
			fmt.Println(line)
		}
	}
	return 0
}

// runPlain is a plain `ssh -N host` with the -L and -R forwards given. It
// waits while $FAKE_SSH_LINK_HOLD exists, then reports the connection and
// sets the forwards up. A forward that fails ends it with 255 when exitOnFail
// is set (ExitOnForwardFailure=yes). It logs its pid to $FAKE_SSH_PID_LOG and
// stays up until TERM or INT, until the file $FAKE_SSH_DROP_DIR/<pid>
// exists, or until its parent process ends (then it exits 255, so a crashed
// test leaves no ssh behind). It polls for that file rather than relying on signals, which
// Windows lacks.
func runPlain(host string, lforwards, rforwards []string, exitOnFail bool, parentGone func() bool) int {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	for _, l := range lforwards {
		if _, _, ok := parseLocalForward(l); !ok {
			fmt.Fprintf(os.Stderr, "Bad local forwarding specification '%s'\n", l)
			return 255
		}
	}
	for exists(os.Getenv("FAKE_SSH_LINK_HOLD")) {
		if parentGone() {
			return 255
		}
		select {
		case <-signals:
			fmt.Fprintln(os.Stderr, "Killed by signal 15.")
			return 255
		case <-time.After(50 * time.Millisecond):
		}
	}
	fmt.Fprintf(os.Stderr, "Authenticated to %s ([127.0.0.1]:22) using \"publickey\".\n", host)

	for _, l := range lforwards {
		bind, port, _ := parseLocalForward(l)
		ln, err := net.Listen("tcp", bind+":"+port)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bind [%s]:%s: Address already in use\n", bind, port)
			if exitOnFail {
				fmt.Fprintln(os.Stderr, "Could not request local forwarding.")
				return 255
			}
			continue
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				c.Close()
			}
		}()
		fmt.Fprintf(os.Stderr, "debug1: Local forwarding listening on %s port %s.\n", bind, port)
	}
	for _, r := range rforwards {
		listen, connect, _ := strings.Cut(r, ":")
		// $FAKE_SSH_RFORWARD_SILENT makes the server never answer a -R:
		// neither success nor refusal is printed, and the process stays up.
		if os.Getenv("FAKE_SSH_RFORWARD_SILENT") != "" {
			continue
		}
		if os.Getenv("FAKE_SSH_RFORWARD_FAIL") != "" {
			fmt.Fprintf(os.Stderr, "Error: remote port forwarding failed for listen path %s\n", listen)
			return 255
		}
		fmt.Fprintf(os.Stderr, "debug1: remote forward success for: listen %s, connect %s\n", listen, connect)
	}

	pid := os.Getpid()
	appendLine(os.Getenv("FAKE_SSH_PID_LOG"), fmt.Sprintf("%d %s", pid, strings.Join(os.Args[1:], " ")))
	drop := ""
	if dir := os.Getenv("FAKE_SSH_DROP_DIR"); dir != "" {
		drop = filepath.Join(dir, strconv.Itoa(pid))
	}
	for {
		select {
		case <-signals:
			fmt.Fprintln(os.Stderr, "Killed by signal 15.")
			return 255
		case <-time.After(50 * time.Millisecond):
		}
		if exists(drop) {
			fmt.Fprintln(os.Stderr, "client_loop: send disconnect: Broken pipe")
			return 255
		}
		if parentGone() {
			return 255
		}
	}
}

// parseLocalForward splits a -L spec, 127.0.0.1:LOCAL:host:REMOTE or
// LOCAL:host:REMOTE, into its bind address and local port. ok is false for
// any other shape.
func parseLocalForward(spec string) (bind, port string, ok bool) {
	f := strings.Split(spec, ":")
	switch len(f) {
	case 4:
		bind, port = f[0], f[1]
	case 3:
		bind, port = "127.0.0.1", f[0]
	default:
		return "", "", false
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return "", "", false
	}
	return bind, port, true
}

// forward answers `-O forward` with the local forward larg or the remote
// forward rarg, failing the ones the FAKE_SSH_*FORWARD* variables name.
func forward(sock, larg, rarg string) int {
	// A remote (-R) forward fails with $FAKE_SSH_RFORWARD_FAIL as the
	// message.
	if msg := os.Getenv("FAKE_SSH_RFORWARD_FAIL"); rarg != "" && msg != "" {
		fmt.Fprintln(os.Stderr, msg)
		return 255
	}
	// Fail only the forward whose local port equals $FAKE_SSH_FORWARD_FAIL.
	if port := os.Getenv("FAKE_SSH_FORWARD_FAIL"); port != "" && strings.HasPrefix(larg, "127.0.0.1:"+port+":") {
		fmt.Fprintf(os.Stderr, "bind [127.0.0.1]:%s: Address already in use\n", port)
		return 255
	}
	// Fail every local forward to the remote port
	// $FAKE_SSH_FORWARD_FAIL_REMOTE.
	if port := os.Getenv("FAKE_SSH_FORWARD_FAIL_REMOTE"); port != "" && strings.HasSuffix(larg, ":localhost:"+port) {
		fmt.Fprintf(os.Stderr, "channel_setup_fwd_listener_tcpip: cannot listen to port: %s\n", port)
		return 255
	}
	// If the file named by $FAKE_SSH_FORWARD_KILLS_MASTER exists, the master
	// dies under this request: remove the file (so it happens once) and the
	// socket (the master exits when it is gone), and fail.
	if f := os.Getenv("FAKE_SSH_FORWARD_KILLS_MASTER"); f != "" && exists(f) {
		os.Remove(f)
		os.Remove(sock)
		fmt.Fprintln(os.Stderr, "mux_client_forward: read from master failed: Broken pipe")
		return 255
	}
	// While the file named by $FAKE_SSH_FORWARD_FAIL_FILE exists, fail the
	// forward whose local port is the file's content.
	if f := os.Getenv("FAKE_SSH_FORWARD_FAIL_FILE"); f != "" && exists(f) {
		b, _ := os.ReadFile(f)
		port := strings.TrimRight(string(b), "\n")
		if strings.HasPrefix(larg, "127.0.0.1:"+port+":") {
			fmt.Fprintf(os.Stderr, "bind [127.0.0.1]:%s: Address already in use\n", port)
			return 255
		}
	}
	return 0
}

// appendLine appends line and a newline to the file at path. An empty path
// or a file that cannot be written is ignored.
func appendLine(path, line string) {
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(line + "\n")
}

// exists reports whether path names a file. An empty path names none.
func exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// touch creates the file at path, or empties it, as the shell's `: > path`
// does.
func touch(path string) {
	if f, err := os.Create(path); err == nil {
		f.Close()
	}
}
