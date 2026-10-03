//go:build !windows

package hostsecret

import (
	"errors"
	"os"
	"path/filepath"
)

func replaceDevelopmentFile(source string, destination string) error {
	return os.Rename(source, destination)
}

func syncSecretParent(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
