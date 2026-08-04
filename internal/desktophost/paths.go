// Package desktophost composes one macOS Desktop daemon generation around the
// shared ProductRuntime. It owns listeners, local capabilities, discovery, and
// generation ownership; it does not assemble stores or data-plane components.
package desktophost

import "github.com/vibe-agi/vibermate/internal/runtimepath"

// Paths contains the Host rendezvous locations derived from the app cache
// directory resolved by the native shell.
type Paths struct {
	layout runtimepath.Layout
}

func NewPaths(appCacheDirectory string) (Paths, error) {
	layout, err := runtimepath.FromAppCache(appCacheDirectory)
	return Paths{layout: layout}, err
}

func (paths Paths) AppCacheDirectory() string {
	return paths.layout.AppCacheDirectory
}

func (paths Paths) RuntimeDirectory() string {
	return paths.layout.RuntimeDirectory
}

func (paths Paths) LockPath() string {
	return paths.layout.GenerationLock
}

func (paths Paths) DiscoveryPath() string {
	return paths.layout.CLIControlRecord
}
