//go:build !windows

package socksproxy

import "syscall"

func udpMessageTruncated(flags int) bool { return flags&syscall.MSG_TRUNC != 0 }
