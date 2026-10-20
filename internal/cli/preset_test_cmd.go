package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/preset"
)

// expectation is a fixture's expected.yaml.
type expectation struct {
	Presets []string `yaml:"presets"`
	Alias   string   `yaml:"alias"`
	Others  []struct {
		Name     string `yaml:"name"`
		Loopback []int  `yaml:"loopback"`
	} `yaml:"others"`
	Routes     []config.Route `yaml:"routes"`
	Advisories []struct {
		ID      string  `yaml:"id"`
		Message *string `yaml:"message"`
		Snippet *string `yaml:"snippet"`
	} `yaml:"advisories"`
}

func newPresetTestCmd() *cobra.Command {
	var presets string
	cmd := &cobra.Command{
		Use:   "test <dir>",
		Short: "Run the preset fixtures in a directory",
		Long: "Each directory in <dir> is a fixture: a sample project and an expected.yaml\n" +
			"that names the presets to enable and lists the routes and advisories they\n" +
			"should produce. Every fixture is run and every difference is reported.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if presets == "" {
				cfgPath, err := configPath(cmd)
				if err != nil {
					return err
				}
				presets = presetsDir(cfgPath)
			}
			return runPresetTest(cmd.OutOrStdout(), args[0], presets)
		},
	}
	cmd.Flags().StringVar(&presets, "presets", "", "directory holding the preset files (default: the presets directory beside the config)")
	return cmd
}

func runPresetTest(out io.Writer, dir, from string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var fixtures []string
	for _, e := range entries {
		// Every directory is a fixture, so that one whose expected.yaml is
		// missing or misnamed fails instead of passing unnoticed.
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			fixtures = append(fixtures, e.Name())
		}
	}
	if len(fixtures) == 0 {
		return fmt.Errorf("no fixtures in %s: a fixture is a directory holding an expected.yaml", dir)
	}
	sort.Strings(fixtures)
	failed := 0
	for _, name := range fixtures {
		problems := runFixture(filepath.Join(dir, name), name, from)
		if len(problems) == 0 {
			fmt.Fprintf(out, "ok    %s\n", name)
			continue
		}
		failed++
		fmt.Fprintf(out, "FAIL  %s\n", name)
		for _, p := range problems {
			fmt.Fprintf(out, "      %s\n", strings.ReplaceAll(p, "\n", "\n      "))
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d fixtures failed", failed, len(fixtures))
	}
	noun := "fixtures"
	if len(fixtures) == 1 {
		noun = "fixture"
	}
	_, err = fmt.Fprintf(out, "%d %s passed.\n", len(fixtures), noun)
	return err
}

// runFixture returns what is wrong with one fixture, or nothing.
// Presets are read from the directory from.
func runFixture(dir, name, from string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "expected.yaml"))
	if errors.Is(err, fs.ErrNotExist) {
		return []string{"no expected.yaml"}
	} else if err != nil {
		return []string{err.Error()}
	}
	var want expectation
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&want); err != nil && !errors.Is(err, io.EOF) {
		return []string{"expected.yaml: " + err.Error()}
	}
	if want.Alias == "" {
		want.Alias = name + ".test"
	}

	var presets []*preset.Preset
	for _, pname := range want.Presets {
		pdata, err := os.ReadFile(filepath.Join(from, pname+".yaml"))
		if err != nil {
			return []string{err.Error()}
		}
		p, err := preset.Parse(pname, pdata)
		if err != nil {
			return []string{err.Error()}
		}
		presets = append(presets, p)
	}
	var others []config.Project
	for _, o := range want.Others {
		p := config.Project{Name: o.Name, Enabled: true}
		for _, port := range o.Loopback {
			p.Routes = append(p.Routes, config.Route{Port: port, Role: config.RoleLoopback})
		}
		others = append(others, p)
	}

	res, err := discovery.Discover(discovery.DirReader{Root: dir}, discovery.Options{Presets: presets})
	if err != nil {
		return []string{err.Error()}
	}
	advisories, warnings := discovery.Advise(res, want.Alias, config.Project{Alias: want.Alias, Routes: res.ConfigRoutes()}.AllHosts(), others)

	var problems []string
	for _, w := range append(res.Warnings, warnings...) {
		problems = append(problems, "warning: "+w)
	}

	var wantRoutes, gotRoutes []string
	for _, r := range want.Routes {
		wantRoutes = append(wantRoutes, routeLine(r))
	}
	for _, r := range res.ConfigRoutes() {
		gotRoutes = append(gotRoutes, routeLine(r))
	}
	if p := difference("routes", wantRoutes, gotRoutes); p != "" {
		problems = append(problems, p)
	}

	var wantIDs, gotIDs []string
	for _, a := range want.Advisories {
		wantIDs = append(wantIDs, a.ID)
	}
	for _, a := range advisories {
		gotIDs = append(gotIDs, a.Code)
	}
	if p := difference("advisories", wantIDs, gotIDs); p != "" {
		return append(problems, p)
	}
	for i, a := range want.Advisories {
		if a.Message != nil && *a.Message != advisories[i].Message {
			problems = append(problems, fmt.Sprintf("advisory %s message\n  want  %s\n  got   %s", a.ID, *a.Message, advisories[i].Message))
		}
		if a.Snippet != nil && strings.TrimRight(*a.Snippet, "\n") != advisories[i].Snippet {
			problems = append(problems, fmt.Sprintf("advisory %s snippet\n  want  %s\n  got   %s", a.ID, strings.TrimRight(*a.Snippet, "\n"), advisories[i].Snippet))
		}
	}
	return problems
}

// difference describes how got differs from want, or returns "".
func difference(what string, want, got []string) string {
	if strings.Join(want, "\n") == strings.Join(got, "\n") {
		return ""
	}
	var b strings.Builder
	b.WriteString(what)
	list := func(tag string, lines []string) {
		if len(lines) == 0 {
			lines = []string{"(none)"}
		}
		for _, line := range lines {
			fmt.Fprintf(&b, "\n  %s  %s", tag, line)
		}
	}
	list("want", want)
	list("got ", got)
	return b.String()
}

// routeLine is how a fixture's route is compared and shown.
func routeLine(r config.Route) string {
	line := fmt.Sprintf("%d %s %s", r.Port, r.Role, r.Label)
	if len(r.Hosts) > 0 {
		line += " hosts=" + strings.Join(r.Hosts, ",")
	}
	return line
}
