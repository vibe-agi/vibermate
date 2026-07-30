//go:build !unix

package instanceguard

import (
	"os"
)

func acquireFile(string) (*os.File, error) {
	return nil, ErrUnsupportedPlatform
}

func releaseFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}
