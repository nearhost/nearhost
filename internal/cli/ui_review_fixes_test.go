package cli

import (
	"context"
	"testing"
	"time"

	"github.com/nearhost/nearhost/internal/config"
	"github.com/nearhost/nearhost/internal/control"
	"github.com/nearhost/nearhost/internal/testutil"
	"github.com/nearhost/nearhost/internal/tui"
)

// The state a real core gives carries the settings of its configuration,
// keep_running and notifications among them, and the quit choice made
// through it honours keep_running: the box is not asked again once the user
// said not to.
func TestCoreStateCarriesKeepRunning(t *testing.T) {
	yes, no := true, false
	r := newClientRun(t, &config.Config{KeepRunning: &yes, Notifications: &no})
	r.u.startDaemon("--started-by", "tui")
	testutil.WaitUntil(t, 30*time.Second, func() bool { _, err := control.Dial(r.u.runDir()); return err == nil }, "the core to answer")
	c, err := control.Dial(r.u.runDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(st.Config))
	if err != nil {
		t.Fatalf("the state's config does not parse: %v", err)
	}
	if cfg.KeepRunning == nil || !*cfg.KeepRunning {
		t.Errorf("the state's keep_running is %v, want true", cfg.KeepRunning)
	}
	if cfg.Notifications == nil || *cfg.Notifications {
		t.Errorf("the state's notifications is %v, want false", cfg.Notifications)
	}
	b := newClientBackend(c, st, &localOps{}, &tui.Proposals{}, true)
	t.Cleanup(b.Close)
	if got := b.QuitChoice(); got != tui.QuitLeave {
		t.Errorf("QuitChoice = %v with keep_running: true, want QuitLeave", got)
	}
}
