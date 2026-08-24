// Package syncengine computes and applies the file-level diff between a source
// worktree's current state and what was last mirrored into a target worktree.
package syncengine

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// FileEntry records enough about a previously-synced file to detect changes without
// re-hashing unchanged files on every cycle.
type FileEntry struct {
	Hash    string      `json:"hash"`
	Mode    os.FileMode `json:"mode"`
	ModTime time.Time   `json:"mod_time"`
	Size    int64       `json:"size"`
}

// Manifest is the persisted record of what was copied into a target on the last sync.
type Manifest struct {
	LastSync time.Time            `json:"last_sync"`
	Files    map[string]FileEntry `json:"files"`
}

// LoadManifest reads the manifest at path, returning an empty one if it doesn't exist
// yet (e.g. the very first sync for this source/target pair).
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Manifest{Files: map[string]FileEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	if m.Files == nil {
		m.Files = map[string]FileEntry{}
	}
	return &m, nil
}

// Save persists the manifest atomically (write-temp-then-rename).
func (m *Manifest) Save(path string) error {
	m.LastSync = time.Now()
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
