// Package runtimedata owns exclusive use and offline relocation of a complete
// runtime data directory. It never deletes the source or changes App settings.
package runtimedata

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	_ "modernc.org/sqlite"
)

const lockName = "runtime-data.lock"

var (
	ErrTarget     = errors.New("storage_target_invalid")
	ErrBusy       = errors.New("storage_in_use")
	ErrCopy       = errors.New("storage_copy_failed")
	ErrValidation = errors.New("storage_validation_failed")
)

// Acquire must outlive every writer, including SQLite and certificate stores.
func Acquire(directory string) (*instanceguard.Guard, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == string(filepath.Separator) {
		return nil, ErrTarget
	}
	// The native shell may have just created this directory using its umask.
	// Apply the same private-directory policy as SQLite before taking the lock.
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrTarget
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return instanceguard.Acquire(filepath.Join(directory, lockName))
}

// Copy creates a NEW child directory at target. Both roots are locked until
// byte verification and SQLite integrity checks finish. On failure a partial
// destination may remain, but the source is intact and stays authoritative.
// The caller must stop its runtime before calling and only select target after
// success. WAL is copied too; SHM and kernel lock files are not persistent data.
func Copy(ctx context.Context, source, target string) error {
	if ctx == nil || !filepath.IsAbs(source) || !filepath.IsAbs(target) ||
		filepath.Clean(source) != source || filepath.Clean(target) != target ||
		source == string(filepath.Separator) || target == string(filepath.Separator) {
		return ErrTarget
	}
	resolvedSource, err := filepath.EvalSymlinks(source)
	if err != nil || resolvedSource != source {
		return ErrTarget
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil || parent != filepath.Dir(target) || within(source, target) || within(target, source) {
		return ErrTarget
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return ErrTarget
	}
	guard, err := Acquire(source)
	if err != nil {
		if errors.Is(err, instanceguard.ErrAlreadyOwned) {
			return ErrBusy
		}
		return ErrCopy
	}
	defer guard.Release()
	// An attached server can hold its host lock beyond the Runtime lifetime.
	serverGuard, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
	if err != nil {
		if errors.Is(err, instanceguard.ErrAlreadyOwned) {
			return ErrBusy
		}
		return ErrCopy
	}
	defer serverGuard.Release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		return ErrTarget
	}
	targetGuard, err := Acquire(target)
	if err != nil {
		return ErrBusy
	}
	defer targetGuard.Release()
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." {
			return err
		}
		if rel == lockName || rel == "server.lock" || rel == "runtime.db-shm" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if info.IsDir() {
			return os.Mkdir(destination, 0o700)
		}
		// Never follow links, devices or sockets out of the selected directory.
		if !info.Mode().IsRegular() {
			return ErrCopy
		}
		return copyVerified(ctx, path, destination, info)
	})
	if err != nil {
		return ErrCopy
	}
	if err := validateDatabase(ctx, filepath.Join(target, "runtime.db")); err != nil {
		return ErrValidation
	}
	// Flush directory entries before reporting a destination safe to select.
	if err := filepath.WalkDir(target, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return err
		}
		directory, err := os.Open(path)
		if err != nil {
			return err
		}
		return errors.Join(directory.Sync(), directory.Close())
	}); err != nil {
		return ErrCopy
	}
	parentDirectory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return ErrCopy
	}
	if err := errors.Join(parentDirectory.Sync(), parentDirectory.Close()); err != nil {
		return ErrCopy
	}
	return nil
}

func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

func copyVerified(ctx context.Context, source, target string, before fs.FileInfo) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return ErrCopy
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600|(before.Mode().Perm()&0o100))
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, hash), &contextReader{ctx, in})
	err = errors.Join(copyErr, out.Sync(), out.Close())
	if err != nil {
		return err
	}
	after, err := in.Stat()
	if err != nil || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return ErrCopy
	}
	check, err := os.Open(target)
	if err != nil {
		return err
	}
	defer check.Close()
	verified := sha256.New()
	if _, err := io.Copy(verified, &contextReader{ctx, check}); err != nil {
		return err
	}
	if string(hash.Sum(nil)) != string(verified.Sum(nil)) {
		return ErrValidation
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func validateDatabase(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrValidation
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return ErrValidation
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrValidation
	}
	return rows.Err()
}
