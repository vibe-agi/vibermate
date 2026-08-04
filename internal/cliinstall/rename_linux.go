//go:build linux

package cliinstall

import "golang.org/x/sys/unix"

func renameNoReplaceAt(directoryFD int, oldName, newName string) error {
	return unix.Renameat2(
		directoryFD,
		oldName,
		directoryFD,
		newName,
		unix.RENAME_NOREPLACE,
	)
}

func exchangeAt(directoryFD int, leftName, rightName string) error {
	return unix.Renameat2(
		directoryFD,
		leftName,
		directoryFD,
		rightName,
		unix.RENAME_EXCHANGE,
	)
}
