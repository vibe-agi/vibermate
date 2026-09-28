package runlauncher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/netip"
	"net/url"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

// Read only local Git metadata, never contact remotes or read commit authors or
// worktree files. Only the sanitized origin identity leaves this process.
// Failure (including missing Git) leaves attribution unknown, not launch failed.
func gitSnapshot(ctx context.Context, cwd string, values []string) *capturerun.GitSnapshot {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	environment := make([]string, 0, len(values))
	for _, value := range values {
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
	value := capturerun.GitSnapshot{RepositorySource: "local", RepositoryKey: hex.EncodeToString(digest[:]), RepositoryName: name}
	value.Branch, ok = read("symbolic-ref", "--quiet", "--short", "HEAD")
	if !ok {
		if _, detached := read("rev-parse", "--verify", "HEAD"); !detached {
			return nil
		}
		value.Detached = true
	}
	if origin, ok := read("config", "--local", "--no-includes", "--get-all", "remote.origin.url"); ok {
		if canonical, label, ok := gitRemoteIdentity(origin); ok {
			digest := sha256.Sum256([]byte("vibermate.git.remote.v1\x00" + canonical))
			value.RepositorySource, value.RepositoryKey, value.RepositoryName = "remote", hex.EncodeToString(digest[:]), label
		}
	}
	if ctx.Err() != nil || value.Validate() != nil {
		return nil
	}
	return &value
}

// Hosting-service SSH/HTTPS forms name the same repository. For other servers,
// retain the transport and SSH path scope: a relative SSH path is not /path.
// SSH aliases, insteadOf rewrites and non-git SSH users are deliberately not
// resolved: that would either require credentials/network or merge wrong repos.
func gitRemoteIdentity(raw string) (canonical, label string, ok bool) {
	if raw == "" || len(raw) > 8192 || strings.ContainsAny(raw, "\x00\r\n") || strings.TrimSpace(raw) != raw {
		return "", "", false
	}
	relativeSSH := false
	if !strings.Contains(raw, "://") {
		host, path, found := strings.Cut(raw, ":")
		if !found || !strings.HasPrefix(host, "git@") || strings.ContainsAny(host, "/\\") || path == "" {
			return "", "", false
		}
		relativeSSH = !strings.HasPrefix(path, "/")
		raw = "ssh://" + host + "/" + strings.TrimPrefix(path, "/")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.Hostname() == "" {
		return "", "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" && scheme != "ssh" && scheme != "git" {
		return "", "", false
	}
	if scheme == "ssh" && (parsed.User == nil || parsed.User.Username() != "git") {
		return "", "", false
	}
	host, port := strings.ToLower(parsed.Hostname()), parsed.Port()
	host = strings.TrimSuffix(host, ".")
	if strings.ContainsAny(host, "@/\\%") || strings.IndexFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", "", false
	}
	// Machine-local aliases/addresses cannot identify the same repository on
	// another device. Do not accidentally merge two users' `git@work:repo`.
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !address.IsGlobalUnicast() || address.IsPrivate() {
			return "", "", false
		}
	} else if !strings.Contains(host, ".") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return "", "", false
	}
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", "", false
		}
		if (scheme == "https" && number == 443) || (scheme == "http" && number == 80) || (scheme == "ssh" && number == 22) || (scheme == "git" && number == 9418) {
			port = ""
		}
	}
	path := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/"), "/")
	publicHost := port == "" && (host == "github.com" || host == "gitlab.com" || host == "bitbucket.org")
	if publicHost {
		path = strings.TrimSuffix(path, ".git")
	}
	if path == "" || strings.ContainsAny(path, "\\?#@:") || strings.IndexFunc(path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", "", false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "~") {
			return "", "", false
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	label = host + "/" + path
	if len(label) > 256 {
		return "", "", false
	}
	if publicHost {
		if host == "github.com" {
			return strings.ToLower(label), label, true
		}
		return label, label, true
	}
	if relativeSSH {
		scheme += "+relative"
	}
	return scheme + "://" + label, label, true
}
