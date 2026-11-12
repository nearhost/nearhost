package cli

import "testing"

// TestLaptopNameFrom checks that the name agents see drops a Windows domain
// or machine prefix, and falls back to "you" when there is no user name.
func TestLaptopNameFrom(t *testing.T) {
	tests := []struct {
		name     string
		username string
		host     string
		want     string
	}{
		{name: "unix user, dotted host", username: "dev", host: "mac.local", want: "dev@mac"},
		{name: "windows machine prefix", username: `hera\ecs`, host: "hera", want: "ecs@hera"},
		{name: "windows domain, no host", username: `CORP\jo`, host: "", want: "jo"},
		{name: "no user name", username: "", host: "box", want: "you@box"},
		{name: "prefix leaves no user name", username: `hera\`, host: "hera", want: "you@hera"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := laptopNameFrom(tt.username, tt.host); got != tt.want {
				t.Errorf("laptopNameFrom(%q, %q) = %q, want %q", tt.username, tt.host, got, tt.want)
			}
		})
	}
}
