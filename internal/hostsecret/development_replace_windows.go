//go:build windows

package hostsecret

import "golang.org/x/sys/windows"

func replaceDevelopmentFile(source string, destination string) error {
	return windows.Rename(source, destination)
}

// windows.Rename uses MoveFileEx with REPLACE_EXISTING, without WRITE_THROUGH.
// Preserve the existing Windows behavior; the durable Server commit contract
// is verified on Unix. Windows cross-compilation does not prove durability.
func syncSecretParent(string) error { return nil }

func syncSecretDirectory(string) error { return nil }
