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
	"github.com/nearhost/nearhost/internal/report"
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
		return []check{problem("core", levelInfo, report.CoreNotRunning, "not running", "start it with nearhost -d, or open nearhost")}, control.Info{}, false
	case err != nil:
		return []check{problem("core", levelInfo, report.DoctorNotChecked, "not checked: "+err.Error(), "")}, control.Info{}, false
	}
	switch {
	case knownAPI(info) != nil:
		// The interface refuses such a core with core.api-mismatch, the
		// code of this row.
		return []check{problem("core", levelWarn, report.CoreAPIMismatch, "speaks a different version of its API", restartCoreFix)}, info, true
	case info.Version != Version:
		checks = append(checks, problem("core", levelWarn, report.CoreOtherVersion, fmt.Sprintf("%s is running; this is %s", printable(info.Version), Version), restartCoreFix))
	default:
		checks = append(checks, check{name: "core", detail: fmt.Sprintf("%s, started %s (pid %d)", printable(info.Version), startedHow(info.StartedBy), info.PID)})
	}
	env, err := c.Environment(ctx)
	if err != nil {
		// An older release has no such route, and a core that cannot look
		// is not a reason to fail: say what was not checked.
		return append(checks, problem("core: environment", levelInfo, report.DoctorNotChecked, "not checked: "+err.Error(), "")), info, true
	}
	for _, e := range env {
		checks = append(checks, envRow(e))
	}
	return checks, info, true
}

// envRow is the row of a check the core makes of its own environment, with
// the code and steps of its problem. A core older than problems sends none.
func envRow(e control.EnvCheck) check {
	c := check{name: "core: " + e.Name, level: envLevel(e.Level), detail: e.Detail, fix: e.Fix}
	if c.level == levelOK || e.Problem == nil {
		return c
	}
	c.code, c.steps = e.Problem.Code, e.Problem.Fix
	if c.fix == "" && len(c.steps) > 0 {
		c.fix = stepLine(c.steps[0])
	}
	return c
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
		return noUserManagerRow(err)
	case errors.Is(err, service.ErrUnreadable):
		return problem("service", levelWarn, report.ServiceUnreadable, "installed, but its definition cannot be read", "nearhost service uninstall, then nearhost service install")
	case err != nil:
		return problem("service", levelWarn, report.DoctorNotChecked, "not checked: "+err.Error(), "run `nearhost doctor` again")
	case !ok:
		return problem("service", levelInfo, report.ServiceNotInstalled, "not installed", "nearhost service install starts Nearhost at login (optional)")
	case b.exe != "" && !samePath(d.Exe, b.exe):
		return problem("service", levelWarn, report.ServiceOtherCopy, fmt.Sprintf("runs %s, not this copy (%s)", d.Exe, b.exe), "run nearhost service install from the copy you use")
	case d.LogPath != "" && folderMissing(filepath.Dir(d.LogPath)):
		// The manager fails the unit before it runs when the folder of the
		// log it sends the core's output to is missing.
		return problem("service", levelWarn, report.ServiceLogFolderMissing, fmt.Sprintf("its log folder %s is missing, so the service cannot start again", printable(filepath.Dir(d.LogPath))), "run nearhost service install")
	}
	active, err := m.Active(ctx)
	switch {
	case errors.Is(err, service.ErrNoUserManager):
		return noUserManagerRow(err)
	case err != nil:
		return problem("service", levelWarn, report.DoctorNotChecked, "not checked: "+err.Error(), "run `nearhost doctor` again")
	case active && !coreRunning:
		// Windows' supervisor keeps the task running while it waits to
		// start a core that stopped.
		return problem("service", levelWarn, report.ServiceRestarting, "installed; it is starting Nearhost again after it stopped", "see why it stopped in core.log; if it does not come back, start it with nearhost -d")
	case active:
		return check{name: "service", detail: "installed and running"}
	case !coreRunning:
		return problem("service", levelWarn, report.ServiceNotRunning, "installed but not running", "start it with nearhost -d")
	}
	return problem("service", levelInfo, report.ServiceTakesOver, fmt.Sprintf("installed; the running Nearhost was started %s, so the service takes over at your next login", startedHow(startedBy)), "")
}

// noUserManagerRow is the service row of a system with no user service
// manager: the message's title, and its steps, as other rows show theirs.
func noUserManagerRow(err error) check {
	m := messageOf(err)
	c := problem("service", levelInfo, m.Code, m.Title, "")
	c.steps = m.Fix
	if len(c.steps) > 0 {
		c.fix = stepLine(c.steps[0])
	}
	return c
}
