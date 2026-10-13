package discovery

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/joho/godotenv"

	"github.com/nearhost/nearhost/internal/config"
)

// Candidate is a discovered port with a proposed role.
type Candidate struct {
	Port   int         // port published on the server
	Target int         // container port when known, else 0
	Role   config.Role // proposed role
	Label  string      // human label, e.g. "Vite (VITE_PORT)"
	Source string      // where it came from: "env:VITE_PORT", "compose:mysql", "vite"
	Var    string      // environment variable that sets it, if any
}

type sailVar struct {
	Key   string
	Label string
	Role  config.Role
}

// sailVars lists the Laravel Sail port variables in display order.
var sailVars = []sailVar{
	{"APP_PORT", "Laravel (APP_PORT)", config.RoleWeb},
	{"VITE_PORT", "Vite (VITE_PORT)", config.RoleLoopback},
	{"FORWARD_DB_PORT", "Database (FORWARD_DB_PORT)", config.RoleIgnore},
	{"FORWARD_REDIS_PORT", "Redis (FORWARD_REDIS_PORT)", config.RoleIgnore},
	{"FORWARD_MAILPIT_PORT", "Mailpit SMTP (FORWARD_MAILPIT_PORT)", config.RoleIgnore},
	{"FORWARD_MAILPIT_DASHBOARD_PORT", "Mailpit dashboard (FORWARD_MAILPIT_DASHBOARD_PORT)", config.RoleIgnore},
	{"FORWARD_MEILISEARCH_PORT", "Meilisearch (FORWARD_MEILISEARCH_PORT)", config.RoleIgnore},
	{"FORWARD_TYPESENSE_PORT", "Typesense (FORWARD_TYPESENSE_PORT)", config.RoleIgnore},
	{"FORWARD_MINIO_PORT", "MinIO (FORWARD_MINIO_PORT)", config.RoleIgnore},
	{"FORWARD_MINIO_CONSOLE_PORT", "MinIO console (FORWARD_MINIO_CONSOLE_PORT)", config.RoleIgnore},
	{"FORWARD_MONGODB_PORT", "MongoDB (FORWARD_MONGODB_PORT)", config.RoleIgnore},
}

func sailVarByName(name string) (sailVar, bool) {
	for _, v := range sailVars {
		if v.Key == name {
			return v, true
		}
	}
	return sailVar{}, false
}

// ParseEnv parses dotenv content.
func ParseEnv(data []byte) (map[string]string, error) {
	env, err := godotenv.Unmarshal(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse env: %w", err)
	}
	for k, v := range env {
		env[k] = strings.TrimSpace(v)
	}
	return env, nil
}

// readEnv reads .env then .env.local, later files overriding earlier ones.
func readEnv(r FileReader) (map[string]string, error) {
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

// envCandidates returns candidates for known Sail variables present in env.
func envCandidates(env map[string]string) []Candidate {
	var out []Candidate
	for _, v := range sailVars {
		raw, ok := env[v.Key]
		if !ok {
			continue
		}
		port, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		out = append(out, Candidate{Port: port, Role: v.Role, Label: v.Label, Source: "env:" + v.Key, Var: v.Key})
	}
	return out
}
