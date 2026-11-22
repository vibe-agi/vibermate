package toolpolicy

import "golang.org/x/sys/unix"

// The platform package is fine here for anything that does not start a
// process.
const openFlags = unix.O_RDONLY | unix.O_NOFOLLOW
