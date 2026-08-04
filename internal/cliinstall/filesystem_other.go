//go:build !darwin && !linux

package cliinstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The managed-link mutation policy is macOS-only. These implementations keep
// policy inspection cross-platform while mutation methods fail closed before
// reaching them.
type anchoredDirectory struct {
	path string
	file *os.File
}

func openAnchoredDirectory(path string, private bool) (*anchoredDirectory, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.IsDir() {
		_ = file.Close()
		return nil, errors.New("path must identify a directory")
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("private installation-record directory is accessible by another user")
	}
	return &anchoredDirectory{path: path, file: file}, nil
}

func ensurePrivateDirectory(path string) (*anchoredDirectory, error) {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, err
	}
	return openAnchoredDirectory(path, true)
}

func (directory *anchoredDirectory) metadata(name string) (entryMetadata, error) {
	info, err := os.Lstat(filepath.Join(directory.path, name))
	if err != nil {
		return entryMetadata{}, err
	}
	kind := entryOther
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		kind = entrySymlink
	case info.Mode().IsRegular():
		kind = entryRegular
	case info.IsDir():
		kind = entryDirectory
	}
	identity, _ := fileIdentity(info)
	return entryMetadata{
		identity:    identity,
		kind:        kind,
		size:        info.Size(),
		permissions: info.Mode().Perm(),
		links:       1,
	}, nil
}

func (directory *anchoredDirectory) openFile(
	name string,
	flags int,
	permissions os.FileMode,
) (*os.File, error) {
	return os.OpenFile(filepath.Join(directory.path, name), flags, permissions)
}

func (directory *anchoredDirectory) readlink(name string) (string, error) {
	return os.Readlink(filepath.Join(directory.path, name))
}

func (directory *anchoredDirectory) symlink(destination, name string) error {
	return os.Symlink(destination, filepath.Join(directory.path, name))
}

func (directory *anchoredDirectory) renameNoReplace(_, _ string) error {
	return errors.New("managed terminal-link mutation is unsupported on this platform")
}

func (directory *anchoredDirectory) exchange(_, _ string) error {
	return errors.New("atomic installation-record refresh is unsupported on this platform")
}

func (directory *anchoredDirectory) unlink(name string) error {
	return os.Remove(filepath.Join(directory.path, name))
}

func (directory *anchoredDirectory) unlinkIfExists(name string) error {
	err := directory.unlink(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (directory *anchoredDirectory) sync() error {
	return directory.file.Sync()
}

func (directory *anchoredDirectory) close() {
	if directory != nil && directory.file != nil {
		_ = directory.file.Close()
		directory.file = nil
	}
}

func fileIdentity(info os.FileInfo) (string, error) {
	if info == nil {
		return "", errors.New("filesystem identity is unavailable")
	}
	return fmt.Sprintf("%016x:%016x", uint64(info.ModTime().UnixNano()), uint64(info.Size()+1)), nil
}

func validFileIdentity(value string) bool {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 16 || len(parts[1]) != 16 {
		return false
	}
	_, leftErr := strconv.ParseUint(parts[0], 16, 64)
	right, rightErr := strconv.ParseUint(parts[1], 16, 64)
	return leftErr == nil && rightErr == nil && right != 0 && strings.ToLower(value) == value
}

func fileOwnedByCurrentUser(os.FileInfo) (bool, error) {
	return true, nil
}

func fileLinkCount(os.FileInfo) (uint64, error) {
	return 1, nil
}
