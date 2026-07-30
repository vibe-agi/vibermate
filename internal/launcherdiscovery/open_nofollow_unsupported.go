//go:build !unix

package launcherdiscovery

import (
	"errors"
	"os"
)

func openReadNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

func validatePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("launcher discovery parent must be a directory")
	}
	return nil
}

func validateOpenedPrivateFile(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("launcher discovery is not a regular file")
	}
	return nil
}
