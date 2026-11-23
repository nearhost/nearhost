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

// Watcher watches a config file for changes made after it was created.
type Watcher struct {
	path  string
	stamp fileStamp // the file as it was when the watcher was made
}

// NewWatcher returns a watcher of path that counts the file as it is now
// as already applied. Make it before the file is read for the config
// first served, so that a change saved after that read is applied once
// Run runs, however late that is.
func NewWatcher(path string) *Watcher {
	return &Watcher{path: path, stamp: stampOf(path)}
}

// Run polls the file every interval and calls apply with the newly
// loaded, validated config after the file's (mtime, size) has changed and
// then stayed the same for one more interval. A load or validation error
// is passed to bad and the last good config stays. It returns when ctx
// ends. A Watcher is run once.
//
// Waiting an interval keeps a save caught half way from being applied. An
// empty file is reported to bad and never applied, because an editor that
// truncates before it writes leaves one for a moment and applying it would
// stop every project. A file that is deleted is ignored for the same reason,
// and one that is created later is applied like any other change.
func (w *Watcher) Run(ctx context.Context, interval time.Duration, apply func(*Config), bad func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	w.runTicks(ctx, ticker.C, nil, apply, bad)
}

// runTicks is Run, polling at each tick from ticks and calling polled,
// when it is not nil, once a poll is done: a test sends the ticks itself and
// waits for each poll, so what a poll sees does not hang on how the machine
// is loaded.
func (w *Watcher) runTicks(ctx context.Context, ticks <-chan time.Time, polled func(), apply func(*Config), bad func(error)) {
	path := w.path
	stamp := w.stamp // the last state handled
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
		if !now.exists {
			stamp = now
			return
		}
		if now.size == 0 {
			stamp = now
			bad(fmt.Errorf("ignored a change to %s: the file is empty", path))
			return
		}
		cfg, err := Load(path)
		// A file deleted or replaced after the stat would load as another,
		// or as an empty config, which stops every project: what was read
		// counts only if the file is still the one that settled.
		if after := stampOf(path); after != now {
			seen = after
			return
		}
		stamp = now
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
