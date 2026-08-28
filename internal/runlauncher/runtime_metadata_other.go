//go:build !darwin && !linux

package runlauncher

func operatingSystemVersion() string { return "" }
