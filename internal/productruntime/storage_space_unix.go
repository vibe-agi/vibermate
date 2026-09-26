//go:build darwin || linux

package productruntime

import (
	"errors"
	"math"

	"golang.org/x/sys/unix"
)

func filesystemAvailableBytes(path string) (uint64, error) {
	var statistics unix.Statfs_t
	if err := unix.Statfs(path, &statistics); err != nil {
		return 0, err
	}
	if statistics.Bsize <= 0 {
		return 0, errors.New("filesystem capacity is invalid")
	}
	blockSize := uint64(statistics.Bsize)
	blocks := uint64(statistics.Bavail)
	if blockSize == 0 || blocks > math.MaxUint64/blockSize {
		return 0, errors.New("filesystem capacity is invalid")
	}
	return blocks * blockSize, nil
}
