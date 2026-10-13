package transport

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestListHostsSkipsWildcardsAndDeduplicates(t *testing.T) {
	cfg := `
Host mini
  HostName 192.168.1.10
  User ecs

Host vps workbox
  HostName example.com

Host *.internal
  ProxyJump bastion

Host mini
  Port 2222

Host !excluded
  User nobody

Host *
  ServerAliveInterval 30
`
	hosts, err := ListHosts(strings.NewReader(cfg))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mini", "vps", "workbox"}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("got %v, want %v", hosts, want)
	}
}

func TestListHostsEmptyConfig(t *testing.T) {
	hosts, err := ListHosts(strings.NewReader(""))
	if err != nil || len(hosts) != 0 {
		t.Fatalf("got %v, %v", hosts, err)
	}
}

func TestDefaultHostsMissingFileIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hosts, err := DefaultHosts()
	if err != nil || hosts != nil {
		t.Fatalf("got %v, %v", hosts, err)
	}
}

func TestDefaultHostsIncludesHostsFromIncludedFiles(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(filepath.Join(dir, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	main := "Include conf.d/*\n\nHost mini\n  HostName 10.0.0.1\n"
	inc := "Host vps\n  HostName example.com\n\nHost *.corp\n  User x\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(main), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "conf.d", "extra"), []byte(inc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	hosts, err := DefaultHosts()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mini", "vps"}
	sort.Strings(hosts)
	sort.Strings(want)
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("got %v, want %v", hosts, want)
	}
}

func writeSSHFile(t *testing.T, home, rel, body string) {
	t.Helper()
	p := filepath.Join(home, ".ssh", rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultHostsIncludeRelativeFile(t *testing.T) {
	home := t.TempDir()
	writeSSHFile(t, home, "config", "Host first\n\nInclude extra\n\nHost last\n")
	writeSSHFile(t, home, "extra", "Host mid *.wild\n  User x\n")
	t.Setenv("HOME", home)
	hosts, err := DefaultHosts()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "mid", "last"}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("got %v, want %v", hosts, want)
	}
}

func TestDefaultHostsIncludeMissingTargetIsNotAnError(t *testing.T) {
	home := t.TempDir()
	writeSSHFile(t, home, "config", "Include nothere conf.d/*\nHost mini\n")
	t.Setenv("HOME", home)
	hosts, err := DefaultHosts()
	if err != nil || !reflect.DeepEqual(hosts, []string{"mini"}) {
		t.Fatalf("got %v, %v", hosts, err)
	}
}

func TestDefaultHostsSelfIncludeTerminates(t *testing.T) {
	home := t.TempDir()
	writeSSHFile(t, home, "config", "Include config\nHost mini\nInclude other\n")
	writeSSHFile(t, home, "other", "Include config\nHost vps\n")
	t.Setenv("HOME", home)
	done := make(chan struct{})
	var hosts []string
	var err error
	go func() {
		defer close(done)
		hosts, err = DefaultHosts()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("include loop did not terminate")
	}
	if err != nil || !reflect.DeepEqual(hosts, []string{"mini", "vps"}) {
		t.Fatalf("got %v, %v", hosts, err)
	}
}
