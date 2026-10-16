package discovery

import (
	"errors"
	"fmt"
	"io/fs"

	"github.com/joho/godotenv"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/preset"
)

// Candidate is a discovered port with a proposed role.
type Candidate struct {
	Port   int         // port published on the server
	Target int         // container port when known, else 0
	Role   config.Role // proposed role
	Label  string      // human label: the Compose service, or a preset's label and variable
	Source string      // where it came from: "compose:<service>", "env:<variable>", "preset:<key>"
	Var    string      // environment variable that sets it, if any
	Preset string      // key of the preset that identified it, if any

	web preset.WebHint
}

// ParseEnv parses dotenv content.
func ParseEnv(data []byte) (map[string]string, error) {
	env, err := godotenv.Unmarshal(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	return env, nil
}

// ReadEnv reads .env then .env.local, later files overriding earlier ones.
func ReadEnv(r FileReader) (map[string]string, error) {
	merged := map[string]string{}
	for _, name := range []string{".env", ".env.local"} {
		data, err := r.ReadFile(name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		env, err := ParseEnv(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		for k, v := range env {
			merged[k] = v
		}
	}
	return merged, nil
}
