//go:build !vibermate_native_secrets && !windows

package hostsecret

import "os"

func replaceDevelopmentFile(source string, destination string) error {
	return os.Rename(source, destination)
}
