//go:build !windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func readDesktopPreferencesFile(path string) (desktopPreferencesFile, error) {
	if err := requirePrivateDesktopPreferencesDirectory(filepath.Dir(path)); err != nil {
		return desktopPreferencesFile{}, err
	}
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return desktopPreferencesFile{}, err
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(path))
	if file == nil {
		_ = unix.Close(descriptor)
		return desktopPreferencesFile{}, errors.New("Desktop preference file is unavailable")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o777 != 0o600 ||
		int(stat.Uid) != os.Geteuid() ||
		stat.Nlink != 1 ||
		stat.Size < 1 || stat.Size > desktopPreferencesStateLimit {
		return desktopPreferencesFile{}, errors.New(
			"Desktop preference file is not a private bounded regular file",
		)
	}
	encoded, err := io.ReadAll(io.LimitReader(file, desktopPreferencesStateLimit+1))
	if err != nil || len(encoded) == 0 || len(encoded) > desktopPreferencesStateLimit {
		return desktopPreferencesFile{}, errors.New("Desktop preference file is unreadable")
	}
	info, err := file.Stat()
	if err != nil {
		return desktopPreferencesFile{}, err
	}
	return desktopPreferencesFile{encoded: encoded, info: info}, nil
}

func requirePrivateDesktopPreferencesDirectory(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&0o777 != 0o700 ||
		int(stat.Uid) != os.Geteuid() {
		return errors.New("Desktop preference directory is not private")
	}
	return nil
}
