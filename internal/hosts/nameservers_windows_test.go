//go:build windows

package hosts

import "testing"

func TestClientConfigOnWindows(t *testing.T) {
	conf, err := clientConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(conf.Servers) == 0 {
		t.Fatal("no nameserver on a connected machine")
	}
	t.Logf("servers: %q", conf.Servers)
}
