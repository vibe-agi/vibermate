//go:build !windows

package workspaceidentity

import "os"

func replaceIdentityFile(source string, destination string) error {
	return os.Rename(source, destination)
}
