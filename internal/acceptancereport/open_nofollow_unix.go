//go:build unix

package acceptancereport

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

func validateOpenedPrivateReport(file *os.File) error {
	var state unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &state); err != nil {
		return err
	}
	if state.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("report is not a regular file")
	}
	if int(state.Uid) != os.Geteuid() {
		return errors.New("report is not owned by the current user")
	}
	if os.FileMode(state.Mode).Perm() != 0o600 {
		return errors.New("report file must have mode 0600")
	}
	return nil
}
