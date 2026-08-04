//go:build windows

package workspaceidentity

import "golang.org/x/sys/windows"

func replaceIdentityFile(source string, destination string) error {
	return windows.Rename(source, destination)
}
