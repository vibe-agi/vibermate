//go:build !windows

package hostsecret

import "os"

func replaceDevelopmentFile(source string, destination string) error {
	return os.Rename(source, destination)
}
