package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

// resolveVLESSExecutable resolves a host-controlled override or fixed sibling.
// Empty,nil means the unbundled daemon has no managed kernel. It never searches
// PATH/downloads or falls back after an explicitly configured path fails.
func resolveVLESSExecutable(explicitPath, daemonExecutable string) (string, error) {
	candidate := explicitPath
	source := "configured VLESS executable"
	optional := explicitPath == ""
	if optional {
		if !filepath.IsAbs(daemonExecutable) || filepath.Clean(daemonExecutable) != daemonExecutable {
			return "", errors.New("VLESS daemon executable path must be clean and absolute")
		}
		daemon, err := filepath.EvalSymlinks(daemonExecutable)
		if err != nil {
			return "", errors.New("VLESS daemon executable path cannot be resolved")
		}
		name := "vibermate-xray"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		candidate = filepath.Join(filepath.Dir(daemon), name)
		source = "packaged VLESS executable"
	}
	if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
		return "", errors.New(source + " path must be clean and absolute")
	}
	// Canonicalize directory aliases (including macOS /var -> /private/var),
	// while leaving the final component unfollowed for the direct-file check.
	parent, err := filepath.EvalSymlinks(filepath.Dir(candidate))
	if err != nil {
		return "", errors.New(source + " parent directory cannot be resolved")
	}
	candidate = filepath.Join(parent, filepath.Base(candidate))
	info, err := os.Lstat(candidate)
	if optional && errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errors.New(source + " is unavailable")
	}
	if !info.Mode().IsRegular() {
		return "", errors.New(source + " must be a regular non-symbolic file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0 {
		return "", errors.New(source + " must have executable permissions")
	}
	return candidate, nil
}
