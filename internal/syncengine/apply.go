package syncengine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ApplyDiff copies/deletes files from sourceDir into targetDir per diff, updating
// manifest in place to reflect the new state. The caller persists manifest afterwards.
func ApplyDiff(sourceDir, targetDir string, diff *Diff, manifest *Manifest) error {
	for _, op := range diff.Ops {
		dst := filepath.Join(targetDir, op.Path)
		switch op.Op {
		case OpDelete:
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("delete %s: %w", op.Path, err)
			}
			delete(manifest.Files, op.Path)
		case OpCopy:
			src := filepath.Join(sourceDir, op.Path)
			if err := copyFile(src, dst, op.Mode); err != nil {
				return fmt.Errorf("copy %s: %w", op.Path, err)
			}
			info, err := os.Lstat(src)
			if err != nil {
				return fmt.Errorf("stat %s after copy: %w", op.Path, err)
			}
			hash, err := hashFile(src)
			if err != nil {
				return fmt.Errorf("hash %s: %w", op.Path, err)
			}
			manifest.Files[op.Path] = FileEntry{Hash: hash, Mode: info.Mode(), ModTime: info.ModTime(), Size: info.Size()}
		case OpSymlink:
			src := filepath.Join(sourceDir, op.Path)
			target, err := os.Readlink(src)
			if err != nil {
				return fmt.Errorf("readlink %s: %w", op.Path, err)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return fmt.Errorf("mkdir for symlink %s: %w", op.Path, err)
			}
			if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove existing %s before symlink: %w", op.Path, err)
			}
			if err := os.Symlink(target, dst); err != nil {
				return fmt.Errorf("symlink %s: %w", op.Path, err)
			}
			info, err := os.Lstat(src)
			if err != nil {
				return fmt.Errorf("stat %s after symlink: %w", op.Path, err)
			}
			hash := sha256.Sum256([]byte(target))
			manifest.Files[op.Path] = FileEntry{Hash: hex.EncodeToString(hash[:]), Mode: info.Mode(), ModTime: info.ModTime(), Size: info.Size()}
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".treesync-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
