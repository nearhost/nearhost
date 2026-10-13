package config

import (
	"os"
	"path/filepath"
)

// Dir is the nearhost configuration directory. A relative XDG_CONFIG_HOME is
// ignored, as the XDG base directory specification requires.
func Dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "nearhost"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "nearhost"), nil
}

func sub(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// DefaultPath is the config file location.
func DefaultPath() (string, error) { return sub("config.yaml") }

// RunDir holds SSH control sockets.
func RunDir() (string, error) { return sub("run") }

// CaddyDir holds Caddy storage including the local CA.
func CaddyDir() (string, error) { return sub("caddy") }
