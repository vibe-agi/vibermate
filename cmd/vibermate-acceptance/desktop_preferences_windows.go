//go:build windows

package main

import "errors"

func readDesktopPreferencesFile(string) (desktopPreferencesFile, error) {
	return desktopPreferencesFile{}, errors.New(
		"packaged Desktop preference inspection is unsupported on Windows",
	)
}

func requirePrivateDesktopPreferencesDirectory(string) error {
	return errors.New(
		"packaged Desktop preference inspection is unsupported on Windows",
	)
}
