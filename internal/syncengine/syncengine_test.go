package syncengine

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// syncOnce runs one diff+apply+save cycle from sourceDir into targetDir using the
// manifest at manifestPath, mirroring what Engine.Sync does internally.
func syncOnce(t *testing.T, sourceDir, targetDir, manifestPath string, present []string) *Diff {
	t.Helper()
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := ComputeDiff(sourceDir, present, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyDiff(sourceDir, targetDir, diff, manifest); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Save(manifestPath); err != nil {
		t.Fatal(err)
	}
	return diff
}

func TestSyncCreateModifyDelete(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	target := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")

	writeFile(t, source, "a.txt", "one")
	diff := syncOnce(t, source, target, manifestPath, []string{"a.txt"})
	if len(diff.Ops) != 1 || diff.Ops[0].Op != OpCopy {
		t.Fatalf("expected one copy op, got %+v", diff.Ops)
	}
	if got := readFile(t, target, "a.txt"); got != "one" {
		t.Fatalf("target a.txt = %q, want %q", got, "one")
	}

	// unchanged file should produce no ops on the next cycle
	diff = syncOnce(t, source, target, manifestPath, []string{"a.txt"})
	if len(diff.Ops) != 0 {
		t.Fatalf("expected no ops for unchanged file, got %+v", diff.Ops)
	}

	// modify
	writeFile(t, source, "a.txt", "two")
	diff = syncOnce(t, source, target, manifestPath, []string{"a.txt"})
	if len(diff.Ops) != 1 || diff.Ops[0].Op != OpCopy {
		t.Fatalf("expected one copy op after modify, got %+v", diff.Ops)
	}
	if got := readFile(t, target, "a.txt"); got != "two" {
		t.Fatalf("target a.txt = %q, want %q", got, "two")
	}

	// delete: a.txt no longer present in source
	diff = syncOnce(t, source, target, manifestPath, []string{})
	if len(diff.Ops) != 1 || diff.Ops[0].Op != OpDelete {
		t.Fatalf("expected one delete op, got %+v", diff.Ops)
	}
	if _, err := os.Stat(filepath.Join(target, "a.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected a.txt removed from target, stat err = %v", err)
	}
}

func TestSyncRecreatesSymlinks(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	target := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")

	writeFile(t, source, "real.txt", "content")
	linkPath := filepath.Join(source, "link.txt")
	if err := os.Symlink(filepath.Join(source, "real.txt"), linkPath); err != nil {
		t.Fatal(err)
	}

	diff := syncOnce(t, source, target, manifestPath, []string{"real.txt", "link.txt"})
	if len(diff.Ops) != 2 {
		t.Fatalf("expected two ops, got %+v", diff.Ops)
	}

	targetLink := filepath.Join(target, "link.txt")
	info, err := os.Lstat(targetLink)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("expected %s to be a symlink in the target", targetLink)
	}
	gotTarget, err := os.Readlink(targetLink)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != filepath.Join(source, "real.txt") {
		t.Fatalf("target link points at %q, want %q", gotTarget, filepath.Join(source, "real.txt"))
	}

	// unchanged symlink should produce no ops on the next cycle
	diff = syncOnce(t, source, target, manifestPath, []string{"real.txt", "link.txt"})
	if len(diff.Ops) != 0 {
		t.Fatalf("expected no ops for unchanged symlink, got %+v", diff.Ops)
	}

	// retarget the symlink
	if err := os.Remove(linkPath); err != nil {
		t.Fatal(err)
	}
	writeFile(t, source, "other.txt", "other")
	if err := os.Symlink(filepath.Join(source, "other.txt"), linkPath); err != nil {
		t.Fatal(err)
	}
	diff = syncOnce(t, source, target, manifestPath, []string{"real.txt", "other.txt", "link.txt"})
	found := false
	for _, op := range diff.Ops {
		if op.Path == "link.txt" {
			found = true
			if op.Op != OpSymlink {
				t.Fatalf("expected retargeted link.txt to produce an OpSymlink op, got %+v", op)
			}
		}
	}
	if !found {
		t.Fatal("expected an op for retargeted link.txt")
	}
	gotTarget, err = os.Readlink(targetLink)
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != filepath.Join(source, "other.txt") {
		t.Fatalf("target link points at %q, want %q", gotTarget, filepath.Join(source, "other.txt"))
	}
}
