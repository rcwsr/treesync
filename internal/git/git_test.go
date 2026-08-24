package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestSplitNul(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single, no trailing nul", "a", []string{"a"}},
		{"single, trailing nul", "a\x00", []string{"a"}},
		{"multiple", "a\x00b\x00c\x00", []string{"a", "b", "c"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := splitNul(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitNul(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// setupRepo creates a repo on branch "main" with one commit, plus a second worktree
// "extra" branched off it, and returns their opened Worktrees.
func setupRepo(t *testing.T) (main, extra *Worktree) {
	t.Helper()
	ctx := context.Background()
	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init", "-q", "-b", "main")
	runGit(t, repoRoot, "config", "user.email", "test@example.com")
	runGit(t, repoRoot, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitignore"), []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoRoot, "add", ".")
	runGit(t, repoRoot, "commit", "-q", "-m", "init")

	extraDir := filepath.Join(t.TempDir(), "extra")
	runGit(t, repoRoot, "worktree", "add", "-q", "-b", "extra-branch", extraDir)

	main, err := Open(ctx, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	extra, err = Open(ctx, extraDir)
	if err != nil {
		t.Fatal(err)
	}
	return main, extra
}

func TestOpenRejectsNonWorktree(t *testing.T) {
	t.Parallel()
	if _, err := Open(context.Background(), t.TempDir()); err == nil {
		t.Fatal("expected Open on a non-git directory to fail")
	}
}

func TestSameRepoAndFindMainWorktree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	main, extra := setupRepo(t)

	same, err := SameRepo(ctx, main, extra)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Fatalf("expected main and extra to be worktrees of the same repo")
	}

	got, err := FindMainWorktree(ctx, extra.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != main.Dir {
		t.Fatalf("FindMainWorktree = %q, want %q", got, main.Dir)
	}
}

func TestCurrentBranchAndHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	main, _ := setupRepo(t)

	branch, err := main.CurrentBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("CurrentBranch = %q, want %q", branch, "main")
	}

	head, err := main.CurrentHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if head == "" {
		t.Fatal("CurrentHead returned empty string")
	}

	if err := main.Detach(ctx); err != nil {
		t.Fatal(err)
	}
	branch, err = main.CurrentBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "" {
		t.Fatalf("CurrentBranch after Detach = %q, want empty (detached)", branch)
	}

	if err := main.Checkout(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	branch, err = main.CurrentBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("CurrentBranch after Checkout = %q, want %q", branch, "main")
	}
}

func TestForceCheckoutAndClean(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	main, _ := setupRepo(t)

	if err := main.Detach(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main.Dir, "README.md"), []byte("modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main.Dir, "untracked.txt"), []byte("junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := main.ForceCheckout(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := main.Clean(ctx); err != nil {
		t.Fatal(err)
	}

	clean, err := main.IsClean(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !clean {
		t.Fatal("expected worktree to be clean after ForceCheckout + Clean")
	}
}

func TestTrackedAndUntrackedFiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	main, _ := setupRepo(t)

	tracked, err := main.TrackedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(tracked, "README.md") {
		t.Fatalf("TrackedFiles = %v, want it to contain README.md", tracked)
	}

	if err := os.WriteFile(filepath.Join(main.Dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(main.Dir, "ignored.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	untracked, err := main.UntrackedFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(untracked, "new.txt") {
		t.Fatalf("UntrackedFiles = %v, want it to contain new.txt", untracked)
	}
	if slices.Contains(untracked, "ignored.txt") {
		t.Fatalf("UntrackedFiles = %v, want it to exclude gitignored ignored.txt", untracked)
	}

	clean, err := main.IsClean(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if clean {
		t.Fatal("expected IsClean to be false with an untracked file present")
	}
}
