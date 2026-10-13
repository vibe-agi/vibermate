package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

type nativePinnedClient struct {
	nativeMCPClientReport
	invocation string
}

func verifyNativeClientVersions(ctx context.Context, clients []nativePinnedClient, node, directory string) error {
	for _, client := range clients {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		command := exec.CommandContext(probe, client.invocation, "--version")
		command.Dir = directory
		command.Env = []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "HOME=" + directory, "CODEX_HOME=" + directory, "CLAUDE_CONFIG_DIR=" + directory}
		command.WaitDelay = time.Second
		output := newBoundedBuffer(8192)
		command.Stdout = output
		command.Stderr = io.Discard
		err := command.Run()
		cancel()
		encoded, overflow := output.snapshot()
		expected := "codex-cli " + client.Version
		if client.Version == "2.1.220" {
			expected = "2.1.220 (Claude Code)"
		}
		if err != nil || overflow || strings.TrimSpace(string(encoded)) != expected {
			return errors.New("native client exact version mismatch")
		}
	}
	return nil
}

func nativePinnedNode(ctx context.Context, directory string) (string, error) {
	node, err := exec.LookPath("node")
	if err != nil || !filepath.IsAbs(node) {
		return "", errors.New("pinned Node unavailable")
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(probe, node, "--version")
	command.Dir = directory
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + directory}
	command.WaitDelay = time.Second
	output := newBoundedBuffer(8192)
	command.Stdout = output
	command.Stderr = io.Discard
	err = command.Run()
	encoded, overflow := output.snapshot()
	if err != nil || overflow || strings.TrimSpace(string(encoded)) != "v22.23.1" {
		return "", errors.New("Node22.23.1 required")
	}
	return node, nil
}

func verifyNativeMCPClients(ctx context.Context, c config, directory string) ([]nativePinnedClient, error) {
	verifier, err := clientadapter.NewReleaseVerifier(clientadapter.BuiltInCatalog())
	if err != nil {
		return nil, err
	}
	var clients []nativePinnedClient
	for _, pin := range []struct{ path, version, source, label string }{
		{c.codex145Path, "0.145.0", "25af12f7e61572b0bc18ddb1008be543b91519b0", "codex"},
		{c.codex159Path, "0.159.2", "ff6aec96948b70d94983af2641a6b67c94faeff5", "codex"},
		{c.codex160Path, "0.160.0", "a956835d020762cb2b570053af06f643a11c0ecc", "codex"},
		{c.claudePath, "2.1.220", "npm:@anthropic-ai/claude-code@2.1.220", "claude"},
	} {
		entry, err := filepath.EvalSymlinks(pin.path)
		if err != nil {
			return nil, errors.New("native client unavailable")
		}
		native := entry
		requestedNative := ""
		if pin.label == "codex" {
			if filepath.Base(entry) == "codex" && filepath.Base(filepath.Dir(entry)) == "bin" {
				requestedNative = entry
				entry = filepath.Clean(filepath.Join(filepath.Dir(entry), "../../../../codex/bin/codex.js"))
			}
			if filepath.Base(entry) != "codex.js" {
				return nil, errors.New("exact Codex package required")
			}
			native = filepath.Clean(filepath.Join(filepath.Dir(entry), "../../codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex"))
			selected := native
			if requestedNative != "" {
				selected = requestedNative
			}
			if err := checkNativeWrapperResolution(entry, selected); err != nil {
				return nil, err
			}
		}
		invocation := filepath.Join(directory, pin.version, pin.label)
		if err := os.MkdirAll(filepath.Dir(invocation), 0700); err != nil {
			return nil, err
		}
		if err := os.Symlink(entry, invocation); err != nil {
			return nil, err
		}
		detection, err := verifier.Verify(ctx, clientadapter.Request{Command: []string{pin.label}, CWD: directory, ExecutablePath: invocation})
		if err != nil || detection.Status != clientadapter.StatusVerified || detection.Evidence == nil || detection.Evidence.Version != pin.version {
			return nil, errors.New("native client identity mismatch")
		}
		file, err := os.Open(native)
		if err != nil {
			return nil, err
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return nil, errors.New("native client hash unavailable")
		}
		clients = append(clients, nativePinnedClient{nativeMCPClientReport: nativeMCPClientReport{Version: pin.version, BinarySHA256: hex.EncodeToString(digest.Sum(nil)), SourcePin: pin.source}, invocation: invocation})
	}
	return clients, nil
}
