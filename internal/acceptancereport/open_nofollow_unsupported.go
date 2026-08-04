//go:build !unix

package acceptancereport

import (
	"errors"
	"os"
)

func openReadNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

func validateOpenedPrivateReport(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("report is not a regular file")
	}
	return nil
}
