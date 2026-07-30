//go:build !vibermate_native_secrets && windows

package hostsecret

import "golang.org/x/sys/windows"

func replaceDevelopmentFile(source string, destination string) error {
	return windows.Rename(source, destination)
}
