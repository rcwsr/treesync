// Package watcher recursively watches a directory tree for changes and emits a
// debounced signal each time a burst of changes settles.
package watcher

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher recursively watches a directory tree and, via Run, emits a debounced
// "something changed" signal on the channel returned by Events.
type FileWatcher struct {
	root     string
	debounce time.Duration
	fs       *fsnotify.Watcher
	changed  chan struct{}
	logger   *slog.Logger
}

// New starts watching root (recursively, skipping .git) and returns a FileWatcher whose
// Run must be called to begin emitting debounced change signals on Events().
func New(root string, debounce time.Duration, logger *slog.Logger) (*FileWatcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	dirs, err := WalkDirs(root)
	if err != nil {
		fw.Close()
		return nil, err
	}
	for _, d := range dirs {
		if err := fw.Add(d); err != nil {
			fw.Close()
			return nil, err
		}
	}
	return &FileWatcher{root: root, debounce: debounce, fs: fw, changed: make(chan struct{}, 1), logger: logger}, nil
}

// Events returns the channel that receives one signal per debounced burst of changes.
// It doesn't report which files changed; callers just re-sync (cheaply, via manifest diff).
func (w *FileWatcher) Events() <-chan struct{} { return w.changed }

// Run consumes fsnotify events until ctx is cancelled, debouncing bursts of changes into
// one signal per settled burst and watching newly-created directories as they appear.
func (w *FileWatcher) Run(ctx context.Context) error {
	defer w.fs.Close()

	dirty := false
	var timer *time.Timer
	var timerC <-chan time.Time

	flush := func() {
		if !dirty {
			return
		}
		dirty = false
		select {
		case w.changed <- struct{}{}:
		case <-ctx.Done():
		}
	}

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return nil
		case ev, ok := <-w.fs.Events:
			if !ok {
				return nil
			}
			dirty = true
			if ev.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					if err := w.fs.Add(ev.Name); err != nil {
						w.logger.Warn("failed to watch new directory", "dir", ev.Name, "err", err)
					}
				}
			}
			if timer == nil {
				timer = time.NewTimer(w.debounce)
			} else {
				timer.Reset(w.debounce)
			}
			timerC = timer.C
		case <-timerC:
			flush()
			timerC = nil
		case err, ok := <-w.fs.Errors:
			if !ok {
				return nil
			}
			w.logger.Error("watcher error", "err", err)
		}
	}
}
