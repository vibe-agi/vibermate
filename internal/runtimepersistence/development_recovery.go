package runtimepersistence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ArchiveUnsupportedDevelopmentDatabase moves one unsupported, unreleased
// SQLite baseline aside without deleting it. The caller may create a clean
// database at the original path only after this function succeeds.
func ArchiveUnsupportedDevelopmentDatabase(
	databasePath string,
	now time.Time,
) (string, error) {
	if databasePath == "" || !filepath.IsAbs(databasePath) ||
		filepath.Clean(databasePath) != databasePath || now.IsZero() {
		return "", fmt.Errorf(
			"%w: development recovery input is invalid",
			ErrInvalidDatabasePath,
		)
	}
	artifacts := []string{databasePath, databasePath + "-wal", databasePath + "-shm"}
	existing := make([]string, 0, len(artifacts))
	for _, path := range artifacts {
		info, err := os.Lstat(path)
		switch {
		case err == nil:
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", fmt.Errorf(
					"%w: SQLite recovery artifact must be a regular file",
					ErrInvalidDatabasePath,
				)
			}
			existing = append(existing, path)
		case errors.Is(err, os.ErrNotExist):
			if path == databasePath {
				return "", fmt.Errorf("inspect development database: %w", err)
			}
		default:
			return "", fmt.Errorf("inspect SQLite recovery artifact: %w", err)
		}
	}
	parent := filepath.Dir(databasePath)
	backupRoot := filepath.Join(parent, "development-backups")
	if err := os.MkdirAll(backupRoot, 0o700); err != nil {
		return "", fmt.Errorf("create development backup root: %w", err)
	}
	if err := os.Chmod(backupRoot, 0o700); err != nil {
		return "", fmt.Errorf("protect development backup root: %w", err)
	}
	backupDirectory := filepath.Join(
		backupRoot,
		"runtime-unsupported-baseline-"+
			now.UTC().Format("20060102T150405.000000000Z"),
	)
	if err := os.Mkdir(backupDirectory, 0o700); err != nil {
		return "", fmt.Errorf("create development database backup: %w", err)
	}

	moved := make([]string, 0, len(existing))
	rollback := func(root error) (string, error) {
		var rollbackErr error
		for index := len(moved) - 1; index >= 0; index-- {
			name := filepath.Base(moved[index])
			if err := os.Rename(
				filepath.Join(backupDirectory, name),
				moved[index],
			); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		_ = os.Remove(backupDirectory)
		return "", errors.Join(root, rollbackErr)
	}
	for _, source := range existing {
		destination := filepath.Join(backupDirectory, filepath.Base(source))
		if err := os.Rename(source, destination); err != nil {
			return rollback(fmt.Errorf("archive development database: %w", err))
		}
		moved = append(moved, source)
		if err := os.Chmod(destination, 0o600); err != nil {
			return rollback(fmt.Errorf("protect archived database: %w", err))
		}
	}
	return backupDirectory, nil
}
