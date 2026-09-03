package desktophost

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/localca"
	"github.com/vibe-agi/vibermate/internal/productruntime"
)

func TestRootResetIntentIsReadOnlyAfterGenerationOwnership(t *testing.T) {
	root := t.TempDir()
	hostPaths, err := NewPaths(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	dataDirectory := filepath.Join(root, "data")
	if err := os.Mkdir(dataDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePaths, err := productruntime.NewRuntimePaths(dataDirectory)
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(dataDirectory, "root-reset-request.json")
	marker := []byte(`{"schema":"invalid"}`)
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	options := DefaultOptions(hostPaths, productruntime.Options{
		Paths:          runtimePaths,
		Host:           hostcontract.Desktop(),
		SecurityRandom: rand.Reader,
	})

	owner, err := instanceguard.Acquire(hostPaths.LockPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), options); !errors.Is(err, instanceguard.ErrAlreadyOwned) {
		t.Fatalf("second generation inspected reset state before ownership: %v", err)
	}
	stored, err := os.ReadFile(markerPath)
	if err != nil || string(stored) != string(marker) {
		t.Fatalf("non-owner changed reset intent: bytes=%q error=%v", stored, err)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}

	if _, err := Start(context.Background(), options); !errors.Is(err, localca.ErrRootResetFailed) {
		t.Fatalf("owning generation did not classify invalid reset intent: %v", err)
	}
	reacquired, err := instanceguard.Acquire(hostPaths.LockPath())
	if err != nil {
		t.Fatalf("failed Root reset retained generation ownership: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatal(err)
	}
}
