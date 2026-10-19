package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
)

// Prepare only the disposable acceptance fixture; both GUI restore launches
// remain independent evidence of the exact packaged App's startup behavior.
func preparePackagedDesktopFixtureData(ctx context.Context, root string) (layout runtimedata.StorageLayout, result error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return layout, err
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return layout, err
	}
	owner, err := runtimedata.Acquire(root)
	if err != nil {
		return layout, err
	}
	defer func() { result = errors.Join(result, owner.Release()) }()
	host, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		return layout, err
	}
	defer func() { result = errors.Join(result, host.Release()) }()
	return runtimedata.PrepareStorageOwned(ctx, runtimedata.OwnedStorage{Directory: root, Runtime: owner, Host: host}, time.Now().UTC(), nil)
}
