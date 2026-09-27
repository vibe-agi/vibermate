package runlauncher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitLaunchSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is not installed")
	}
	root := filepath.Join(t.TempDir(), "example repo")
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "core.hooksPath=" + t.TempDir()}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "-b", "main")
	ctx := context.Background()
	first := gitSnapshot(ctx, filepath.Join(root, "nested"))
	if first == nil || first.RepositoryName != "example repo" || first.Branch != "main" || first.Detached {
		t.Fatalf("unborn nested repo: %#v", first)
	}
	git("commit", "--allow-empty", "-m", "fixture")
	worktree := filepath.Join(t.TempDir(), "other checkout")
	git("worktree", "add", "-b", "feature/review", worktree)
	other := gitSnapshot(ctx, worktree)
	if other == nil || other.RepositoryKey != first.RepositoryKey || other.RepositoryName != first.RepositoryName || other.Branch != "feature/review" {
		t.Fatalf("worktree must share repo but not branch: %#v", other)
	}
	git("checkout", "--detach")
	detached := gitSnapshot(ctx, root)
	if detached == nil || !detached.Detached || detached.Branch != "" || first.Branch != "main" {
		t.Fatal("snapshot changed or detached HEAD was lost")
	}
	if got := gitSnapshot(ctx, t.TempDir()); got != nil {
		t.Fatal("non-Git directory became a project")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := gitSnapshot(canceled, root); got != nil {
		t.Fatal("canceled probe returned attribution")
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "unrelated"))
	if got := gitSnapshot(ctx, root); got == nil || got.RepositoryKey != first.RepositoryKey {
		t.Fatal("ambient Git environment changed the launch directory attribution")
	}
}
