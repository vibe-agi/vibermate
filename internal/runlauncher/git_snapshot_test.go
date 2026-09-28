package runlauncher

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
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
	first := gitSnapshot(ctx, filepath.Join(root, "nested"), os.Environ())
	if first == nil || first.RepositorySource != "local" || first.RepositoryName != "example repo" || first.Branch != "main" || first.Detached {
		t.Fatalf("unborn nested repo: %#v", first)
	}
	git("commit", "--allow-empty", "-m", "fixture")
	worktree := filepath.Join(t.TempDir(), "other checkout")
	git("worktree", "add", "-b", "feature/review", worktree)
	other := gitSnapshot(ctx, worktree, os.Environ())
	if other == nil || other.RepositoryKey != first.RepositoryKey || other.RepositoryName != first.RepositoryName || other.Branch != "feature/review" {
		t.Fatalf("worktree must share repo but not branch: %#v", other)
	}
	git("checkout", "--detach")
	detached := gitSnapshot(ctx, root, os.Environ())
	if detached == nil || !detached.Detached || detached.Branch != "" || first.Branch != "main" {
		t.Fatal("snapshot changed or detached HEAD was lost")
	}
	if got := gitSnapshot(ctx, t.TempDir(), os.Environ()); got != nil {
		t.Fatal("non-Git directory became a project")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if got := gitSnapshot(canceled, root, os.Environ()); got != nil {
		t.Fatal("canceled probe returned attribution")
	}
	git("remote", "add", "origin", "https://private-user:private-token@GitHub.com/team/project.git?token=secret#private")
	remote := gitSnapshot(ctx, root, os.Environ())
	if remote == nil || remote.RepositorySource != "remote" || remote.RepositoryName != "github.com/team/project" || remote.RepositoryKey == first.RepositoryKey {
		t.Fatalf("sanitized origin: %#v", remote)
	}
	launchHome := t.TempDir()
	launcher := &Launcher{config: Config{BaseEnvironment: append(os.Environ(), "USER=launch-user", "USERNAME=launch-user", "HOME="+launchHome, "USERPROFILE="+launchHome, "TZ=UTC", "OPENAI_API_KEY=should-not-leak")}}
	command := []string{"agent", "resume"}
	create := launcher.captureCreateRequest(ctx, root, "/agent", command, environment.SystemTransparentID)
	command[1] = "changed"
	if create.RuntimeMetadata.GitAtLaunch == nil || create.RuntimeMetadata.GitAtLaunch.RepositoryKey != remote.RepositoryKey || create.RuntimeMetadata.LocalUserName != "launch-user" || create.RuntimeMetadata.TimeZone != "UTC" || create.Command[1] != "resume" || create.EnvironmentInventory == nil || create.EnvironmentInventory.Validate() != nil || create.ClientEnvironment == nil || !create.ClientEnvironment.OpenAIAPIKeyPresent {
		t.Fatalf("incomplete shared launch context: %+v", create)
	}
	data, err := json.Marshal(create)
	if err != nil || strings.Contains(string(data), "should-not-leak") || strings.Contains(string(data), "private-token") {
		t.Fatal("launch context leaked an environment value or remote credential")
	}
	if got := gitSnapshot(ctx, worktree, os.Environ()); got == nil || got.RepositoryKey != remote.RepositoryKey || got.Branch != "feature/review" {
		t.Fatalf("remote worktree: %#v", got)
	}
	git("remote", "set-url", "origin", "git@github.com:team/project.git")
	renamed := root + " renamed"
	if err := os.Rename(root, renamed); err != nil {
		t.Fatal(err)
	}
	root = renamed
	if got := gitSnapshot(ctx, root, os.Environ()); got == nil || got.RepositoryKey != remote.RepositoryKey || got.RepositoryName != remote.RepositoryName {
		t.Fatalf("renamed SSH clone split project: %#v", got)
	}
	clone := filepath.Join(t.TempDir(), "different-name")
	if err := os.Mkdir(clone, 0700); err != nil {
		t.Fatal(err)
	}
	oldRoot := root
	root = clone
	git("init", "-b", "main")
	git("remote", "add", "origin", "ssh://git@github.com:22/team/project.git")
	if got := gitSnapshot(ctx, clone, os.Environ()); got == nil || got.RepositoryKey != remote.RepositoryKey {
		t.Fatalf("different clone split project: %#v", got)
	}
	root = oldRoot
	git("config", "--add", "remote.origin.url", "https://github.com/other/project.git")
	if got := gitSnapshot(ctx, root, os.Environ()); got == nil || got.RepositorySource != "local" {
		t.Fatalf("ambiguous origins were merged: %#v", got)
	}
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "unrelated"))
	if got := gitSnapshot(ctx, root, os.Environ()); got == nil || got.RepositorySource != "local" {
		t.Fatal("ambient Git environment changed the launch directory attribution")
	}
}

func TestGitRemoteIdentity(t *testing.T) {
	for _, test := range []struct{ raw, canonical, label string }{
		{"https://user:secret@GitHub.com/team/project.git?secret=value#secret", "github.com/team/project", "github.com/team/project"},
		{"git@github.com:team/project.git", "github.com/team/project", "github.com/team/project"},
		{"git@github.com:Team/Project.git", "github.com/team/project", "github.com/Team/Project"},
		{"ssh://git@github.com:22/team/project.git", "github.com/team/project", "github.com/team/project"},
		{"https://gitlab.com/group/sub/project.git/", "gitlab.com/group/sub/project", "gitlab.com/group/sub/project"},
		{"git@bitbucket.org:other/project.git", "bitbucket.org/other/project", "bitbucket.org/other/project"},
		{"https://git.example.test:8443/team/project.git", "https://git.example.test:8443/team/project.git", "git.example.test:8443/team/project.git"},
		{"git@git.example.test:team/project.git", "ssh+relative://git.example.test/team/project.git", "git.example.test/team/project.git"},
		{"ssh://git@git.example.test/team/project.git", "ssh://git.example.test/team/project.git", "git.example.test/team/project.git"},
		{"ssh://git:secret@[2001:db8::1]:2222/team/project.git", "ssh://[2001:db8::1]:2222/team/project.git", "[2001:db8::1]:2222/team/project.git"},
	} {
		t.Run(test.raw, func(t *testing.T) {
			key, label, ok := gitRemoteIdentity(test.raw)
			if !ok || key != test.canonical || label != test.label || strings.Contains(key+label, "secret") {
				t.Fatalf("identity=%q label=%q ok=%v", key, label, ok)
			}
		})
	}
	for _, raw := range []string{"", "./local/repo", "file:///local/repo", "C:\\repo", "ext::helper private", "alice@server:project", "ssh://alice@server/project", "ssh://server/project", "git@work:project", "https://localhost/repo", "https://127.0.0.1/repo", "https://10.0.0.2/repo", "ssh://git@[::1]/repo", "https://[::ffff:127.0.0.1]/repo", "https://host.local/repo", "git@host.example:~bob/repo", "https://host.example/../repo", "https://host.example/%0Arepo", "https://host.example:99999/repo", "https://host.example/project\nhttps://host.example/other", "https://host.example/" + strings.Repeat("x", 257)} {
		if _, _, ok := gitRemoteIdentity(raw); ok {
			t.Fatalf("ambiguous or unsafe origin accepted: %q", raw)
		}
	}
}
