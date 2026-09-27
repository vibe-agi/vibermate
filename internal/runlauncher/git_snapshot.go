package runlauncher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

// Read only local Git metadata, never remotes, commit authors or worktree files.
// Failure (including missing Git) leaves attribution unknown, not launch failed.
func gitSnapshot(ctx context.Context, cwd string) *capturerun.GitSnapshot {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	environment := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GIT_") {
			environment = append(environment, value)
		}
	}
	read := func(args ...string) (string, bool) {
		command := exec.CommandContext(ctx, "git", append([]string{"-C", cwd}, args...)...)
		command.Env = environment
		command.WaitDelay = 100 * time.Millisecond
		pipe, err := command.StdoutPipe()
		if err != nil || command.Start() != nil {
			return "", false
		}
		output, readErr := io.ReadAll(io.LimitReader(pipe, 8193))
		if readErr != nil || len(output) > 8192 {
			cancel()
		}
		if err := command.Wait(); err != nil || readErr != nil || len(output) > 8192 {
			return "", false
		}
		return strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r"), true
	}
	common, ok := read("rev-parse", "--path-format=absolute", "--git-common-dir")
	if !ok || !filepath.IsAbs(common) {
		return nil
	}
	common, err := filepath.EvalSymlinks(common)
	if err != nil {
		return nil
	}
	name := filepath.Base(common)
	if name == ".git" {
		name = filepath.Base(filepath.Dir(common))
	}
	digest := sha256.Sum256([]byte(common))
	value := capturerun.GitSnapshot{RepositoryKey: hex.EncodeToString(digest[:]), RepositoryName: name}
	value.Branch, ok = read("symbolic-ref", "--quiet", "--short", "HEAD")
	if !ok {
		if _, detached := read("rev-parse", "--verify", "HEAD"); !detached {
			return nil
		}
		value.Detached = true
	}
	if ctx.Err() != nil || value.Validate() != nil {
		return nil
	}
	return &value
}
