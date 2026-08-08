//go:build !windows

package main

import (
	"errors"
	"io"
	"os"

	"github.com/vibe-agi/vibermate/internal/secretstore"
	"golang.org/x/sys/unix"
)

const acceptanceCredentialLimit = int64(64 << 10)

// readPrivateCredential is intentionally separate from flag parsing. A secret
// file is opened only for the opt-in credentialed phase, without following a
// symlink, and its bytes are immediately transferred to a destroyable Value.
func readPrivateCredential(path string) (*secretstore.Value, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("provider credential file is unavailable")
	}
	if !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 ||
		before.Size() <= 0 || before.Size() > acceptanceCredentialLimit {
		return nil, errors.New("provider credential file must be a bounded regular 0600 file")
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("provider credential file could not be opened safely")
	}
	opened := os.NewFile(uintptr(fd), path)
	if opened == nil {
		_ = unix.Close(fd)
		return nil, errors.New("provider credential file could not be opened safely")
	}
	defer opened.Close()
	afterOpen, err := opened.Stat()
	if err != nil || !afterOpen.Mode().IsRegular() ||
		!os.SameFile(before, afterOpen) {
		return nil, errors.New("provider credential file changed while opening")
	}
	payload, err := io.ReadAll(io.LimitReader(opened, acceptanceCredentialLimit+1))
	if err != nil || len(payload) == 0 || int64(len(payload)) > acceptanceCredentialLimit {
		clear(payload)
		return nil, errors.New("provider credential file could not be read safely")
	}
	afterRead, err := opened.Stat()
	if err != nil || !os.SameFile(afterOpen, afterRead) ||
		afterRead.Size() != int64(len(payload)) {
		clear(payload)
		return nil, errors.New("provider credential file changed while reading")
	}
	value, err := secretstore.NewValue(payload)
	clear(payload)
	if err != nil {
		return nil, errors.New("provider credential file contains an invalid value")
	}
	return value, nil
}
