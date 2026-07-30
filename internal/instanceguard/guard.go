// Package instanceguard owns the operating-system lock that serializes one
// Desktop daemon generation for a user-private runtime directory.
package instanceguard

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrAlreadyOwned        = errors.New("runtime generation is already owned")
	ErrUnsupportedPlatform = errors.New("runtime generation lock is unsupported on this platform")
)

// Guard owns one kernel-backed generation lock. The stable lock file is never
// removed; releasing the open file description relinquishes ownership.
type Guard struct {
	path string
	file *os.File

	mu          sync.RWMutex
	releaseOnce sync.Once
	releaseErr  error
}

// Acquire validates an explicit lock-file path and takes its generation lock
// without waiting. A caller must retain the returned Guard for the full Host
// lifetime.
func Acquire(path string) (*Guard, error) {
	if path == "" ||
		!filepath.IsAbs(path) ||
		filepath.Clean(path) != path ||
		filepath.Base(path) == "." ||
		filepath.Base(path) == string(filepath.Separator) {
		return nil, errors.New("generation lock path must be an absolute clean file path")
	}
	file, err := acquireFile(path)
	if err != nil {
		return nil, err
	}
	return &Guard{path: path, file: file}, nil
}

func (guard *Guard) Path() string {
	if guard == nil {
		return ""
	}
	return guard.path
}

// OwnsDirectory reports whether this live kernel lock authorizes publication
// in the lock file's private runtime directory.
func (guard *Guard) OwnsDirectory(directory string) bool {
	if guard == nil ||
		directory == "" ||
		!filepath.IsAbs(directory) ||
		filepath.Clean(directory) != directory ||
		filepath.Dir(guard.path) != directory {
		return false
	}
	guard.mu.RLock()
	defer guard.mu.RUnlock()
	return guard.file != nil
}

// Release is idempotent and leaves the stable lock file in place.
func (guard *Guard) Release() error {
	if guard == nil {
		return nil
	}
	guard.releaseOnce.Do(func() {
		guard.mu.Lock()
		file := guard.file
		guard.file = nil
		guard.mu.Unlock()
		guard.releaseErr = releaseFile(file)
	})
	return guard.releaseErr
}
