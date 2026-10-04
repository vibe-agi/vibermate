package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
)

var errNativeSourceUnbound = errors.New("native scenario observations are not bound to clean source")

func nativeSourceProvenance(ctx context.Context, directory string) (string, map[string]string, error) {
	git := func(arguments ...string) (string, error) {
		command := exec.CommandContext(ctx, "git", arguments...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		output := newBoundedBuffer(64 << 10)
		command.Stdout = output
		command.Stderr = output
		err := command.Run()
		data, overflow := output.snapshot()
		if err != nil || overflow {
			return "", errors.New("native source provenance unavailable")
		}
		return strings.TrimSpace(string(data)), nil
	}
	root, err := git("rev-parse", "--show-toplevel")
	if err != nil || !filepath.IsAbs(root) {
		return "", nil, errors.New("native source checkout unavailable")
	}
	sha, err := git("rev-parse", "HEAD")
	decoded, decodeErr := hex.DecodeString(sha)
	if err != nil || decodeErr != nil || len(decoded) != 20 {
		return "", nil, errors.New("native source SHA unavailable")
	}
	fixtures := map[string]string{}
	for _, name := range []string{"pinned_clients.json", "codex-0.145-initialize.synthetic.json", "codex-0.159-initialize.synthetic.json", "codex-0.160-callbacks.synthetic.json", "recorded-webp.base64"} {
		path := filepath.Join(root, "internal", "nativemcp", "testdata", name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return sha, fixtures, errors.New("native fixture provenance unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return sha, fixtures, errors.New("native fixture provenance unavailable")
		}
		digest := sha256.Sum256(data)
		fixtures[name] = hex.EncodeToString(digest[:])
	}
	dirty, err := git("status", "--porcelain", "--untracked-files=normal")
	if err != nil || dirty != "" {
		return sha, fixtures, errors.New("native source checkout is not clean")
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return sha, fixtures, errors.New("native build provenance unavailable")
	}
	revision, modified := "", ""
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	if revision != sha || modified != "false" {
		return sha, fixtures, errors.New("native build does not match clean source")
	}
	return sha, fixtures, nil
}

func nativeBuildMatchesSource(sha string) bool {
	decoded, err := hex.DecodeString(sha)
	if err != nil || len(decoded) != 20 || strings.ToLower(sha) != sha {
		return false
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	revision, modified := "", ""
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	return revision == sha && modified == "false"
}
