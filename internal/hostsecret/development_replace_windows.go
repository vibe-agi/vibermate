//go:build windows

package hostsecret

import "golang.org/x/sys/windows"

func replaceDevelopmentFile(source string, destination string) error {
	return windows.Rename(source, destination)
}
