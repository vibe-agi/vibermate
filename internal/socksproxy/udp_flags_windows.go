//go:build windows

package socksproxy

import "golang.org/x/sys/windows"

func udpMessageTruncated(flags int) bool { return flags&windows.MSG_TRUNC != 0 }
