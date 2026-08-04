//go:build !windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func readDesktopNavigationFile(path string) (desktopNavigationFile, error) {
	if err := requirePrivateDesktopNavigationDirectory(filepath.Dir(path)); err != nil {
		return desktopNavigationFile{}, err
	}
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return desktopNavigationFile{}, err
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(path))
	if file == nil {
		_ = unix.Close(descriptor)
		return desktopNavigationFile{}, errors.New("Desktop navigation file is unavailable")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil ||
		stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&0o777 != 0o600 ||
		int(stat.Uid) != os.Geteuid() ||
		stat.Nlink != 1 ||
		stat.Size < 1 || stat.Size > desktopNavigationStateLimit {
		return desktopNavigationFile{}, errors.New(
			"Desktop navigation file is not a private bounded regular file",
		)
	}
	encoded, err := io.ReadAll(io.LimitReader(file, desktopNavigationStateLimit+1))
	if err != nil || len(encoded) == 0 || len(encoded) > desktopNavigationStateLimit {
		return desktopNavigationFile{}, errors.New("Desktop navigation file is unreadable")
	}
	info, err := file.Stat()
	if err != nil {
		return desktopNavigationFile{}, err
	}
	return desktopNavigationFile{encoded: encoded, info: info}, nil
}

func requirePrivateDesktopNavigationDirectory(path string) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&0o777 != 0o700 ||
		int(stat.Uid) != os.Geteuid() {
		return errors.New("Desktop navigation directory is not private")
	}
	return nil
}
