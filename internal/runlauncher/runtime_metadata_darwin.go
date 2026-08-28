//go:build darwin

package runlauncher

import "golang.org/x/sys/unix"

func operatingSystemVersion() string {
	version, err := unix.Sysctl("kern.osproductversion")
	if err == nil && version != "" {
		return version
	}
	version, _ = unix.Sysctl("kern.osrelease")
	return version
}
