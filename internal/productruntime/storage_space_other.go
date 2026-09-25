//go:build !darwin && !linux && !windows

package productruntime

import "errors"

func filesystemAvailableBytes(string) (uint64, error) {
	return 0, errors.New("filesystem capacity is unavailable")
}
