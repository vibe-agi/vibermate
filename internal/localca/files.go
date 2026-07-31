package localca

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type atomicTempFile interface {
	io.Writer
	Chmod(os.FileMode) error
	Sync() error
	Close() error
	Name() string
}

type atomicFileOperations interface {
	CreateTemp(string, string) (atomicTempFile, error)
	Rename(string, string) error
	Remove(string) error
	SyncDirectory(string) error
}

type systemAtomicFileOperations struct{}

func (systemAtomicFileOperations) CreateTemp(
	directory, pattern string,
) (atomicTempFile, error) {
	return os.CreateTemp(directory, pattern)
}

func (systemAtomicFileOperations) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (systemAtomicFileOperations) Remove(path string) error {
	return os.Remove(path)
}

func (systemAtomicFileOperations) SyncDirectory(path string) error {
	return syncDirectory(path)
}

// replacePrivateAtomic persists one complete private file in the same
// directory as its destination. A crash can expose either the previous file
// or the complete replacement, never a partially overwritten destination.
func replacePrivateAtomic(
	operations atomicFileOperations,
	path string,
	data []byte,
) error {
	if operations == nil || path == "" || filepath.Base(path) == "." {
		return errors.New("atomic private-file replacement is invalid")
	}
	directory := filepath.Dir(path)
	temporary, err := operations.CreateTemp(
		directory,
		"."+filepath.Base(path)+".*.tmp",
	)
	if err != nil {
		return fmt.Errorf("create manifest migration file: %w", err)
	}
	temporaryPath := temporary.Name()
	renamed := false
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if !renamed {
			_ = operations.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set manifest migration permissions: %w", err)
	}
	if err := writeAll(temporary, data); err != nil {
		return fmt.Errorf("write manifest migration file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync manifest migration file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return fmt.Errorf("close manifest migration file: %w", err)
	}
	closed = true
	if err := operations.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Root manifest: %w", err)
	}
	renamed = true
	if err := operations.SyncDirectory(directory); err != nil {
		return fmt.Errorf("sync Root manifest directory: %w", err)
	}
	return nil
}

func writeAll(destination io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := destination.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(data) {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create local CA directory: %w", err)
		}
		info, err = os.Lstat(path)
	case err != nil:
		return fmt.Errorf("inspect local CA directory: %w", err)
	}
	if err != nil {
		return fmt.Errorf("inspect created local CA directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: local CA path is not a directory", ErrRootStateInvalid)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf(
			"%w: local CA directory permissions must be 0700",
			ErrRootStateInvalid,
		)
	}
	return nil
}

func requirePrivateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect local CA file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: local CA file is not regular", ErrRootStateInvalid)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf(
			"%w: local CA file permissions must be 0600",
			ErrRootStateInvalid,
		)
	}
	return nil
}

func writeExclusive(path string, data []byte, permissions os.FileMode) error {
	file, err := os.OpenFile(
		path,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		permissions,
	)
	if err != nil {
		return fmt.Errorf("create local CA file: %w", err)
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write local CA file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync local CA file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close local CA file: %w", err)
	}
	complete = true
	return nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open local CA file: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read local CA file: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: local CA file exceeds size limit", ErrRootStateInvalid)
	}
	return data, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open local CA directory: %w", err)
	}
	defer func() {
		_ = directory.Close()
	}()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync local CA directory: %w", err)
	}
	return nil
}
