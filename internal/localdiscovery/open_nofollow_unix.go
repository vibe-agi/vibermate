//go:build unix

package localdiscovery

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func openReadNoFollow(path string) (*os.File, error) {
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func validatePrivateDirectory(path string) error {
	descriptor, err := unix.Open(
		path,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return err
	}
	defer unix.Close(descriptor)
	var state unix.Stat_t
	if err := unix.Fstat(descriptor, &state); err != nil {
		return err
	}
	if state.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("local control discovery parent is not a directory")
	}
	if int(state.Uid) != os.Geteuid() {
		return errors.New("local control discovery directory is not owned by the current user")
	}
	if os.FileMode(state.Mode).Perm() != 0o700 {
		return errors.New("local control discovery directory must have mode 0700")
	}
	return nil
}

func validateOpenedPrivateFile(file *os.File) error {
	var state unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &state); err != nil {
		return err
	}
	if state.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("local control discovery is not a regular file")
	}
	if int(state.Uid) != os.Geteuid() {
		return errors.New("local control discovery file is not owned by the current user")
	}
	if os.FileMode(state.Mode).Perm() != 0o600 {
		return errors.New("local control discovery file must have mode 0600")
	}
	return nil
}
