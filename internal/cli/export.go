package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/discovery"
	"github.com/nearhost/nearhost/internal/nearhostrc"
	"github.com/nearhost/nearhost/internal/transport"
)

// exportBackend is the seam between `export` and the servers it writes to.
type exportBackend struct {
	// openRemote opens the project directory on a server for reading and
	// writing, on one connection, and returns the directory it opened. The
	// returned function ends the connection. Nil means remote projects
	// cannot be exported, which only tests that need no server use.
	openRemote func(ctx context.Context, srv config.Server, projectPath string) (discovery.FileReader, transport.FileWriter, string, func() error, error)
}

// systemExport is the production backend. Only NewRoot wires it in.
func systemExport() exportBackend {
	return exportBackend{openRemote: sshClient{}.openRemoteWritable}
}

type exportFlags struct {
	dryRun, force bool
}

func newExportCmd(b exportBackend) *cobra.Command {
	var flags exportFlags
	cmd := &cobra.Command{
		Use:   "export [project]",
		Short: "Write a project's setup to its .nearhostrc, to commit with the project",
		Long: "Write what Nearhost serves for a project (its routes, hosts and main name)\n" +
			"to a .nearhostrc in the project's directory, so that whoever adds the\n" +
			"project after cloning it gets the same setup. Without a name, export picks\n" +
			"the project on this machine whose directory holds the current one. A port\n" +
			"follows its variable in .env when discovery finds one that sets it. The file\n" +
			"is checked to read back to the same project before it is written, and an\n" +
			"existing one is replaced only with --force.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd, b, flags, args)
		},
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "print the file instead of writing it")
	cmd.Flags().BoolVar(&flags.force, "force", false, "replace an existing .nearhostrc")
	return cmd
}

func runExport(cmd *cobra.Command, b exportBackend, flags exportFlags, args []string) error {
	cfg, cfgPath, err := loadConfig(cmd)
	if err != nil {
		return err
	}
	p, err := exportTarget(cfg, cfgPath, args)
	if err != nil {
		return err
	}
	reader, writer, dir, done, err := openForExport(cmd.Context(), b, cfg, p)
	if err != nil {
		return err
	}
	data, snap, pending, err := renderExport(cmd, cfg, cfgPath, p, reader, dir, flags)
	if err == nil && !flags.dryRun {
		err = writer.WriteFile(nearhostrc.FileName, data)
	}
	if err := errors.Join(err, done()); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if flags.dryRun {
		_, err := out.Write(data)
		return err
	}
	// The file is written first: a snapshot saved for a file that failed
	// to be written would approve what is not there.
	if err := saveSnapshot(cfgPath, p.Name, snap); err != nil {
		return fmt.Errorf("wrote %s, but saving the config failed: %w", fileIn(p, dir), err)
	}
	if pending {
		fmt.Fprintln(out, "replaced the .nearhostrc change that was waiting for approval with what is served now")
	}
	where := ""
	if p.Server != config.LocalServer {
		where = " on server " + p.Server
	}
	_, err = fmt.Fprintf(out, "Wrote %s%s. Commit it so others get this setup when they add the project.\n", fileIn(p, dir), where)
	return err
}

// exportTarget is the project export works on: the one named, local or
// remote, or else the local project whose directory holds the current one.
// When projects are nested, the deepest holds it: the current directory is
// in that one's tree more than in its parent's.
func exportTarget(cfg *config.Config, cfgPath string, args []string) (config.Project, error) {
	if len(args) == 1 {
		p, ok := cfg.Project(args[0])
		if !ok {
			return config.Project{}, fmt.Errorf("project %q is not in %s", args[0], cfgPath)
		}
		return *p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return config.Project{}, err
	}
	var best *config.Project
	for i := range cfg.Projects {
		p := &cfg.Projects[i]
		if p.Server != config.LocalServer || !holds(p.Path, wd) {
			continue
		}
		if best == nil || len(filepath.Clean(p.Path)) > len(filepath.Clean(best.Path)) {
			best = p
		}
	}
	if best == nil {
		return config.Project{}, fmt.Errorf("no Nearhost project contains %s; add it with nearhost add", wd)
	}
	return *best, nil
}

// holds reports whether dir is root or under it. The paths are compared as
// given and then with symbolic links resolved, since the working directory
// may name the project through a link (macOS's /tmp is /private/tmp) that
// the saved path does not, or the other way round.
func holds(root, dir string) bool {
	under := func(root, dir string) bool {
		rel, err := filepath.Rel(root, dir)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if under(root, dir) {
		return true
	}
	r, err1 := filepath.EvalSymlinks(root)
	d, err2 := filepath.EvalSymlinks(dir)
	return err1 == nil && err2 == nil && under(r, d)
}

// openForExport opens p's directory for reading and writing: this machine's
// files for a local project, an SFTP connection of its own for a remote one.
// It returns the directory opened, which for a remote project is the one
// found on the server.
func openForExport(ctx context.Context, b exportBackend, cfg *config.Config, p config.Project) (discovery.FileReader, transport.FileWriter, string, func() error, error) {
	if p.Server == config.LocalServer {
		return discovery.DirReader{Root: p.Path}, transport.DirWriter(p.Path), p.Path, func() error { return nil }, nil
	}
	srv, ok := cfg.Server(p.Server)
	if !ok {
		return nil, nil, "", nil, fmt.Errorf("project %q is on server %q, which is not in the config", p.Name, p.Server)
	}
	if b.openRemote == nil {
		return nil, nil, "", nil, fmt.Errorf("cannot reach server %s", p.Server)
	}
	return b.openRemote(ctx, srv, p.Path)
}

// renderExport works out the file for p from its files: the port variables
// discovery finds with the enabled presets, as add finds them, and the
// project's env. It refuses an existing file unless forced or only shown,
// and reports whether that file was a change waiting for approval, as the
// review classifies it, which the export will replace.
func renderExport(cmd *cobra.Command, cfg *config.Config, cfgPath string, p config.Project, reader discovery.FileReader, dir string, flags exportFlags) ([]byte, *config.RcSnapshot, bool, error) {
	existing, found, err := nearhostrc.Read(reader)
	if err != nil && !found {
		return nil, nil, false, err
	}
	if found && !flags.force && !flags.dryRun {
		return nil, nil, false, fmt.Errorf("%s already exists in %s; run with --force to replace it, or --dry-run to see what would be written", nearhostrc.FileName, dir)
	}
	// Only a change the review would hold for approval was waiting: one
	// that only moves ports, or a file that does not parse, waits for no
	// one.
	pending := nearhostrc.Classify(p, existing, found, err).Kind == nearhostrc.Pending

	res, warnings, err := discoverOnce(cfg, cfgPath, reader, nil, nil)
	if err != nil {
		return nil, nil, false, err
	}
	// Warnings go to standard error, so a dry run's output is the file and
	// nothing else.
	printWarnings(cmd.ErrOrStderr(), warnings)
	vars := nearhostrc.PortVars{}
	for _, c := range res.Routes {
		if c.Var != "" {
			vars[c.Port] = append(vars[c.Port], c.Var)
		}
	}
	env, err := discovery.ReadEnv(reader)
	if err != nil {
		return nil, nil, false, err
	}
	data, snap, err := nearhostrc.Export(p, baseName(p, dir), vars, env)
	if err != nil {
		return nil, nil, false, err
	}
	return data, snap, pending, nil
}

// baseName is the name of the project's directory, by the path rules of the
// machine it is on.
func baseName(p config.Project, dir string) string {
	if p.Server == config.LocalServer {
		return filepath.Base(dir)
	}
	return path.Base(dir)
}

// fileIn is the path of p's .nearhostrc in dir, by the path rules of the
// machine it is on.
func fileIn(p config.Project, dir string) string {
	if p.Server == config.LocalServer {
		return filepath.Join(dir, nearhostrc.FileName)
	}
	return path.Join(dir, nearhostrc.FileName)
}

// saveSnapshot sets the approved snapshot of the project called name to snap
// in the config at cfgPath and saves it, under the config's lock. Nothing
// but the snapshot changes, since the file was rendered from what the config
// already serves.
func saveSnapshot(cfgPath, name string, snap *config.RcSnapshot) error {
	return config.Update(cfgPath, func(cfg *config.Config) error {
		p, ok := cfg.Project(name)
		if !ok {
			return fmt.Errorf("project %q is no longer in %s", name, cfgPath)
		}
		p.Nearhostrc = snap
		return nil
	})
}
