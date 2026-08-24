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

func TestSyncRejectsSymlinks(t *testing.T) {
	t.Parallel()
	source := t.TempDir()
	target := t.TempDir()
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")

	writeFile(t, source, "real.txt", "content")
	if err := os.Symlink(filepath.Join(source, "real.txt"), filepath.Join(source, "link.txt")); err != nil {
		t.Fatal(err)
	}

	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeDiff(source, []string{"real.txt", "link.txt"}, manifest); err == nil {
		t.Fatal("expected ComputeDiff to reject a symlink, got nil error")
	}
	_ = target
}
