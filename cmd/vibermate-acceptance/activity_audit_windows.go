//go:build windows

package main

import (
	"errors"
	"os"
)

func requirePrivateAuditOwnership(os.FileInfo, bool) error {
	return errors.New("private audit path ownership is unsupported on Windows")
}
