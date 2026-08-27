package syncengine

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/rcwsr/treesync/internal/git"
)

// Engine orchestrates one sync pass from source into target, persisting a manifest
// under stateDir so repeated syncs are incremental.
type Engine struct {
	Source       *git.Worktree
	Target       *git.Worktree
	ManifestPath string
}

// NewEngine builds an Engine that persists its manifest under stateDir.
func NewEngine(source, target *git.Worktree, stateDir string) *Engine {
	return &Engine{Source: source, Target: target, ManifestPath: filepath.Join(stateDir, "manifest.json")}
}

// Result summarizes one Sync call.
type Result struct {
	Created  int
	Modified int
	Deleted  int
	Duration time.Duration
}

// Sync enumerates source's tracked+untracked (non-gitignored) files, diffs them against
// the last-synced manifest, and applies the result to target.
func (e *Engine) Sync(ctx context.Context) (*Result, error) {
	start := time.Now()

	present, err := presentFiles(ctx, e.Source)
	if err != nil {
		return nil, err
	}
	manifest, err := LoadManifest(e.ManifestPath)
	if err != nil {
		return nil, err
	}
	diff, err := ComputeDiff(e.Source.Dir, present, manifest)
	if err != nil {
		return nil, err
	}
	if len(diff.Ops) == 0 {
		return &Result{Duration: time.Since(start)}, nil
	}
	// Count before ApplyDiff runs: it rewrites the manifest in place, which would
	// corrupt the created-vs-modified classification below.
	created, modified, deleted := countOps(diff, manifest)
	if err := ApplyDiff(e.Source.Dir, e.Target.Dir, diff, manifest); err != nil {
		return nil, err
	}
	if err := manifest.Save(e.ManifestPath); err != nil {
		return nil, err
	}
	return &Result{Created: created, Modified: modified, Deleted: deleted, Duration: time.Since(start)}, nil
}

// countOps tallies the diff into Result counters. Copy and symlink ops both bring a
// source file into the target, so neither is a deletion; they split into created vs
// modified by whether the path was already recorded in the manifest (which the caller
// must evaluate before ApplyDiff mutates it).
func countOps(diff *Diff, manifest *Manifest) (created, modified, deleted int) {
	for _, op := range diff.Ops {
		switch op.Op {
		case OpDelete:
			deleted++
		case OpCopy, OpSymlink:
			if _, ok := manifest.Files[op.Path]; ok {
				modified++
			} else {
				created++
			}
		}
	}
	return created, modified, deleted
}

// presentFiles returns source's tracked + untracked-but-not-ignored files that still
// exist on disk (git ls-files already excludes gitignored paths; a tracked file deleted
// from the working tree without `git rm` still appears in ls-files, so we stat it out).
func presentFiles(ctx context.Context, w *git.Worktree) ([]string, error) {
	tracked, err := w.TrackedFiles(ctx)
	if err != nil {
		return nil, err
	}
	untracked, err := w.UntrackedFiles(ctx)
	if err != nil {
		return nil, err
	}
	all := append(tracked, untracked...)
	present := all[:0]
	for _, rel := range all {
		if _, err := os.Lstat(filepath.Join(w.Dir, rel)); err == nil {
			present = append(present, rel)
		}
	}
	return present, nil
}
