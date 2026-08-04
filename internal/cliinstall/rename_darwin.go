//go:build darwin

package cliinstall

import "golang.org/x/sys/unix"

func renameNoReplaceAt(directoryFD int, oldName, newName string) error {
	return unix.RenameatxNp(
		directoryFD,
		oldName,
		directoryFD,
		newName,
		unix.RENAME_EXCL,
	)
}

func exchangeAt(directoryFD int, leftName, rightName string) error {
	return unix.RenameatxNp(
		directoryFD,
		leftName,
		directoryFD,
		rightName,
		unix.RENAME_SWAP,
	)
}
