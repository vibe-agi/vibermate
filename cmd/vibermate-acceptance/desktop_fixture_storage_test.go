package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
)

func TestPackagedDesktopPreferenceSeedHasLegitimateRequiredStorage(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := desktopPreferencesStatePath(home)
	root := filepath.Dir(filepath.Dir(path))
	layout, err := preparePackagedDesktopFixtureData(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := publishDesktopPreferencesFixture(path, staleDesktopPreferences())
	if err != nil {
		t.Fatal(err)
	}
	declared, err := runtimedata.ReadStorageLayout(root)
	if err != nil || declared.ID != layout.ID {
		t.Fatalf("preferences seeded before real required storage: %+v %v", declared, err)
	}
	// Reacquire both guards, exactly as subsequent App startup does. The SDK
	// fixture must have released them before preference publication/GUI launch.
	owner, err := runtimedata.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	host, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
	again, err := runtimedata.PrepareStorageOwned(t.Context(), runtimedata.OwnedStorage{Directory: root, Runtime: owner, Host: host}, time.Now().UTC(), nil)
	if err != nil || again.ID != layout.ID {
		t.Fatalf("actual required startup refused preference seed: %+v %v", again, err)
	}
	after, err := readDesktopPreferencesFile(path)
	if err != nil || string(after.encoded) != string(seed.encoded) {
		t.Fatal("storage preparation changed preference fixture", err)
	}
}

func TestPackagedDesktopFixtureStorageRefusesExistingUnknownState(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "ui-state"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = preparePackagedDesktopFixtureData(context.Background(), root); !errors.Is(err, runtimedata.ErrFreshControlAmbiguous) {
		t.Fatalf("preexisting state bypassed fresh-control refusal: %v", err)
	}
	// Failure must release both guards as well.
	owner, err := runtimedata.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()
	host, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer host.Release()
}
