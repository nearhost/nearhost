package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/service"
)

// coreCallTimeout bounds doctor's calls to the core: the core's own
// environment probes take up to 10 s, and this leaves slack for the dial.
const coreCallTimeout = 12 * time.Second

const restartCoreFix = "restart it: nearhost stop, then nearhost -d (nearhost update does this after an update)"

// checkCore reports the running core: "core", then "core: <name>" for each
// check the core makes of its own environment. It also returns what the
// core said about itself, and whether one answered, for the service check.
func checkCore(ctx context.Context, b doctorBackend) (checks []check, info control.Info, answered bool) {
	if b.dial == nil {
		return nil, control.Info{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, coreCallTimeout)
	defer cancel()
	c, err := b.dial()
	if err == nil {
		info, err = c.Info(ctx)
	}
	switch {
	case errors.Is(err, control.ErrNotRunning):
		return []check{{"core", levelInfo, "not running", "start it with nearhost -d, or open nearhost"}}, control.Info{}, false
	case err != nil:
		return []check{{"core", levelInfo, "not checked: " + err.Error(), ""}}, control.Info{}, false
	}
	switch {
	case info.API != control.APIVersion:
		return []check{{"core", levelWarn, "speaks a different version of its API", restartCoreFix}}, info, true
	case info.Version != Version:
		checks = append(checks, check{"core", levelWarn, fmt.Sprintf("%s is running; this is %s", printable(info.Version), Version), restartCoreFix})
	default:
		checks = append(checks, check{name: "core", detail: fmt.Sprintf("%s, started by %s (pid %d)", printable(info.Version), printable(string(info.StartedBy)), info.PID)})
	}
	env, err := c.Environment(ctx)
	if err != nil {
		// An older release has no such route, and a core that cannot look
		// is not a reason to fail: say what was not checked.
		return append(checks, check{"core: environment", levelInfo, "not checked: " + err.Error(), ""}), info, true
	}
	for _, e := range env {
		checks = append(checks, check{"core: " + e.Name, envLevel(e.Level), e.Detail, e.Fix})
	}
	return checks, info, true
}

// envLevel is doctor's level of the same name as one the core reports.
func envLevel(s string) level {
	switch s {
	case "ok":
		return levelOK
	case "info":
		return levelInfo
	case "fail":
		return levelFail
	}
	return levelWarn
}

// folderMissing reports whether dir is known not to exist: a folder that
// cannot be looked at is not said to be missing.
func folderMissing(dir string) bool {
	_, err := os.Stat(dir)
	return errors.Is(err, fs.ErrNotExist)
}

// checkService reports the login service. Its name is empty when this
// system has no service manager to ask, and the check is left out.
func checkService(ctx context.Context, b doctorBackend, coreRunning bool, startedBy control.StartedBy) check {
	if b.service == nil {
		return check{}
	}
	m := b.service()
	if m == nil {
		return check{}
	}
	d, ok, err := m.Installed(ctx)
	switch {
	case errors.Is(err, service.ErrUnsupported):
		return check{}
	case errors.Is(err, service.ErrNoUserManager):
		return check{"service", levelInfo, err.Error(), ""}
	case errors.Is(err, service.ErrUnreadable):
		return check{"service", levelWarn, "installed, but its definition cannot be read", "nearhost service uninstall, then nearhost service install"}
	case err != nil:
		return check{"service", levelWarn, "not checked: " + err.Error(), "run `nearhost doctor` again"}
	case !ok:
		return check{"service", levelInfo, "not installed", "nearhost service install starts Nearhost at login (optional)"}
	case b.exe != "" && !samePath(d.Exe, b.exe):
		return check{"service", levelWarn, fmt.Sprintf("runs %s, not this copy (%s)", d.Exe, b.exe), "run nearhost service install from the copy you use"}
	case d.LogPath != "" && folderMissing(filepath.Dir(d.LogPath)):
		// The manager fails the unit before it runs when the folder of the
		// log it sends the core's output to is missing.
		return check{"service", levelWarn, fmt.Sprintf("its log folder %s is missing, so the service cannot start", printable(filepath.Dir(d.LogPath))), "run nearhost service install"}
	}
	active, err := m.Active(ctx)
	switch {
	case errors.Is(err, service.ErrNoUserManager):
		return check{"service", levelInfo, err.Error(), ""}
	case err != nil:
		return check{"service", levelWarn, "not checked: " + err.Error(), "run `nearhost doctor` again"}
	case active:
		return check{name: "service", detail: "installed and running"}
	case !coreRunning:
		return check{"service", levelWarn, "installed but not running", "start it with nearhost -d"}
	}
	return check{"service", levelInfo, fmt.Sprintf("installed; the running Nearhost was started by %s, so the service takes over at your next login", printable(string(startedBy))), ""}
}
