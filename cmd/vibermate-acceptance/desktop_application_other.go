//go:build !darwin

package main

import "errors"

func inspectDesktopProcess(int) (desktopProcessSnapshot, error) {
	return desktopProcessSnapshot{}, errors.New(
		"packaged Desktop process binding requires macOS",
	)
}
