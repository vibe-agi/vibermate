package localca

import (
	"errors"
	"fmt"
	"io"
	"os"
)

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
