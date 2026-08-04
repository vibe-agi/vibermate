//go:build windows

package main

import "errors"

func readDesktopNavigationFile(string) (desktopNavigationFile, error) {
	return desktopNavigationFile{}, errors.New(
		"packaged Desktop navigation inspection is unsupported on Windows",
	)
}

func requirePrivateDesktopNavigationDirectory(string) error {
	return errors.New(
		"packaged Desktop navigation inspection is unsupported on Windows",
	)
}
