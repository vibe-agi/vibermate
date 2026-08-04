//go:build !windows

package main

import (
	"errors"
	"os"
	"syscall"
)

func requirePrivateAuditOwnership(info os.FileInfo, directory bool) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || (!directory && stat.Nlink != 1) {
		return errors.New("private audit path ownership is invalid")
	}
	return nil
}
