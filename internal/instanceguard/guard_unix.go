//go:build unix

package instanceguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func acquireFile(path string) (*os.File, error) {
	directory := filepath.Dir(path)
	directoryDescriptor, err := openPrivateDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer unix.Close(directoryDescriptor)
	descriptor, err := unix.Openat(
		directoryDescriptor,
		filepath.Base(path),
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, fmt.Errorf("open generation lock: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), path)
	fail := func(root error) (*os.File, error) {
		return nil, errors.Join(root, file.Close())
	}
	var state unix.Stat_t
	if err := unix.Fstat(descriptor, &state); err != nil {
		return fail(fmt.Errorf("inspect generation lock: %w", err))
	}
	if state.Mode&unix.S_IFMT != unix.S_IFREG || int(state.Uid) != os.Geteuid() {
		return fail(errors.New("generation lock must be a regular file owned by the current user"))
	}
	if err := unix.Fchmod(descriptor, 0o600); err != nil {
		return fail(fmt.Errorf("secure generation lock: %w", err))
	}
	if err := unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, ErrAlreadyOwned
		}
		return fail(fmt.Errorf("acquire generation lock: %w", err))
	}
	if err := file.Sync(); err != nil {
		_ = unix.Flock(descriptor, unix.LOCK_UN)
		return fail(fmt.Errorf("sync generation lock: %w", err))
	}
	if err := unix.Fsync(directoryDescriptor); err != nil {
		_ = unix.Flock(descriptor, unix.LOCK_UN)
		return fail(fmt.Errorf("sync generation lock directory: %w", err))
	}
	return file, nil
}

func releaseFile(file *os.File) error {
	if file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func openPrivateDirectory(path string) (int, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return -1, fmt.Errorf("create generation lock directory: %w", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return -1, fmt.Errorf("secure generation lock directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return -1, fmt.Errorf("inspect generation lock directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return -1, errors.New("generation lock parent must be a non-symlink directory")
	}
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return -1, fmt.Errorf("open generation lock directory: %w", err)
	}
	var state unix.Stat_t
	if err := unix.Fstat(descriptor, &state); err != nil {
		_ = unix.Close(descriptor)
		return -1, fmt.Errorf("inspect opened generation lock directory: %w", err)
	}
	if state.Mode&unix.S_IFMT != unix.S_IFDIR ||
		int(state.Uid) != os.Geteuid() {
		_ = unix.Close(descriptor)
		return -1, errors.New("generation lock directory is not owned by the current user")
	}
	if os.FileMode(state.Mode).Perm() != 0o700 {
		_ = unix.Close(descriptor)
		return -1, errors.New("generation lock directory must have mode 0700")
	}
	return descriptor, nil
}
