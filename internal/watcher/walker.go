package watcher

import (
	"io/fs"
	"path/filepath"
)

// WalkDirs returns every directory under root, skipping .git.
func WalkDirs(root string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs, err
}
