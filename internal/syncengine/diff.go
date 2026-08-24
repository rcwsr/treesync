package syncengine

import (
	"fmt"
	"os"
	"path/filepath"
)

// Op identifies what a FileOp does to the target.
type Op string

const (
	// OpCopy copies/overwrites a file in the target from the source.
	OpCopy Op = "copy"
	// OpDelete removes a file from the target that is no longer present in the source.
	OpDelete Op = "delete"
)

// FileOp is one file-level action to apply to the target.
type FileOp struct {
	Op   Op
	Path string // relative to both the source and target worktree roots
	Mode os.FileMode
}

// Diff is the set of file operations needed to bring the target up to date with source.
type Diff struct {
	Ops []FileOp
}

// ComputeDiff compares the source worktree's currently-present files (present) against
// the last-synced manifest, skipping any file whose size/mode/mtime hasn't changed, and
// producing delete ops for manifest entries no longer present in the source.
func ComputeDiff(sourceDir string, present []string, manifest *Manifest) (*Diff, error) {
	diff := &Diff{}
	seen := make(map[string]struct{}, len(present))
	for _, rel := range present {
		seen[rel] = struct{}{}
		full := filepath.Join(sourceDir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			continue // vanished between enumeration and stat; the next cycle will see the deletion
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("treesync does not support symlinks yet: %s", rel)
		}
		if prev, ok := manifest.Files[rel]; ok &&
			prev.Size == info.Size() && prev.Mode == info.Mode() && prev.ModTime.Equal(info.ModTime()) {
			continue
		}
		diff.Ops = append(diff.Ops, FileOp{Op: OpCopy, Path: rel, Mode: info.Mode()})
	}
	for rel := range manifest.Files {
		if _, ok := seen[rel]; !ok {
			diff.Ops = append(diff.Ops, FileOp{Op: OpDelete, Path: rel})
		}
	}
	return diff, nil
}
