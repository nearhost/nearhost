package config

import (
	"context"
	"fmt"
	"os"
	"time"
)

// fileStamp is what Watch checks to notice a change to the config file.
type fileStamp struct {
	exists bool
	mod    time.Time
	size   int64
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, mod: info.ModTime(), size: info.Size()}
}

// Watch polls path every interval and calls apply with the newly loaded,
// validated config after the file's (mtime, size) has changed and then
// stayed the same for one more interval. A load or validation error is
// passed to bad and the last good config stays. It returns when ctx ends.
//
// Waiting an interval keeps a save caught half way from being applied. An
// empty file is reported to bad and never applied, because an editor that
// truncates before it writes leaves one for a moment and applying it would
// stop every project. A file that is deleted is ignored for the same reason,
// and one that is created later is applied like any other change. The file
// as it is when Watch starts counts as already applied.
func Watch(ctx context.Context, path string, interval time.Duration, apply func(*Config), bad func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	watchTicks(ctx, path, ticker.C, nil, apply, bad)
}

// watchTicks is Watch, polling at each tick from ticks and calling polled,
// when it is not nil, once a poll is done: a test sends the ticks itself and
// waits for each poll, so what a poll sees does not hang on how the machine
// is loaded.
func watchTicks(ctx context.Context, path string, ticks <-chan time.Time, polled func(), apply func(*Config), bad func(error)) {
	stamp := stampOf(path) // the last state handled
	seen := stamp          // the state at the previous tick
	poll := func() {
		now := stampOf(path)
		if now == stamp {
			seen = now
			return
		}
		if now != seen {
			seen = now // changed since the last tick: wait for it to settle
			return
		}
		stamp = now
		if !now.exists {
			return
		}
		if now.size == 0 {
			bad(fmt.Errorf("ignored a change to %s: the file is empty", path))
			return
		}
		cfg, err := Load(path)
		if err == nil {
			err = cfg.Validate()
		}
		if err != nil {
			bad(err)
			return
		}
		apply(cfg)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
		}
		poll()
		if polled != nil {
			polled()
		}
	}
}
