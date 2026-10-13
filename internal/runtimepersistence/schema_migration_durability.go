package runtimepersistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

// The original source (never its inspection copy) must cross successful file
// and directory durability boundaries before independent commit proof.
func (runner schemaMigrationRunner) syncOwnedSource(ctx context.Context, v VerifiedMigrationDatabase) error {
	if ctx.Err() != nil || !migrationOwners(v) {
		return ErrMigrationOutcomeUnknown
	}
	// Inspection copies cannot establish whether a rollback journal committed.
	// Preserve any such unexpected artifact for recovery rather than activating.
	if _, err := os.Lstat(v.sourcePath + "-journal"); !errors.Is(err, os.ErrNotExist) {
		return ErrMigrationOutcomeUnknown
	}
	var files []schemaInspectionFile
	for _, suffix := range []string{"", "-wal"} {
		path := v.sourcePath + suffix
		info, err := os.Lstat(path)
		if suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return ErrMigrationOutcomeUnknown
		}
		files = append(files, schemaInspectionFile{path: path, info: info})
	}
	syncFile := runner.syncSourceFile
	if syncFile == nil {
		syncFile = syncMigrationSourceFile
	}
	for _, file := range files {
		if ctx.Err() != nil || !migrationOwners(v) {
			return ErrMigrationOutcomeUnknown
		}
		if err := syncFile(ctx, file.path); err != nil {
			return ErrMigrationOutcomeUnknown
		}
	}
	if ctx.Err() != nil || !migrationOwners(v) {
		return ErrMigrationOutcomeUnknown
	}
	syncDirectory := runner.syncSourceDirectory
	if syncDirectory == nil {
		syncDirectory = syncMigrationSourceDirectory
	}
	if err := syncDirectory(ctx, filepath.Dir(v.sourcePath)); err != nil {
		return ErrMigrationOutcomeUnknown
	}
	for _, file := range files {
		info, err := os.Lstat(file.path)
		if err != nil || !sameInspectionFile(file.info, info) {
			return ErrMigrationOutcomeUnknown
		}
	}
	if len(files) == 1 {
		if _, err := os.Lstat(v.sourcePath + "-wal"); !errors.Is(err, os.ErrNotExist) {
			return ErrMigrationOutcomeUnknown
		}
	}
	if _, err := os.Lstat(v.sourcePath + "-journal"); !errors.Is(err, os.ErrNotExist) {
		return ErrMigrationOutcomeUnknown
	}
	if ctx.Err() != nil || !migrationOwners(v) {
		return ErrMigrationOutcomeUnknown
	}
	return nil
}

func syncMigrationSourceFile(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrMigrationOutcomeUnknown
	}
	// Existing-file read/write handles permit FlushFileBuffers on Windows too;
	// there is no create, truncation, SQLite recovery or journal-mode change.
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return ErrMigrationOutcomeUnknown
	}
	opened, err := file.Stat()
	if err != nil || !sameInspectionFile(info, opened) {
		_ = file.Close()
		return ErrMigrationOutcomeUnknown
	}
	if err := ctx.Err(); err != nil {
		_ = file.Close()
		return err
	}
	err = errors.Join(file.Sync(), file.Close())
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !sameInspectionFile(info, after) {
		return ErrMigrationOutcomeUnknown
	}
	return ctx.Err()
}

func syncMigrationSourceDirectory(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return ErrMigrationOutcomeUnknown
	}
	directory, err := os.Open(path)
	if err != nil {
		return ErrMigrationOutcomeUnknown
	}
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(info, opened) {
		_ = directory.Close()
		return ErrMigrationOutcomeUnknown
	}
	if err := ctx.Err(); err != nil {
		_ = directory.Close()
		return err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !after.IsDir() || !os.SameFile(info, after) {
		return ErrMigrationOutcomeUnknown
	}
	return ctx.Err()
}
