//go:build darwin

package main

import (
	"context"
	"database/sql"
	"errors"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/vibe-agi/vibermate/internal/environment"
)

func TestPackagedACPScopeRendezvousRequiresActualMirror(t *testing.T) {
	root, publish := packagedACPFixtureState(t, false)
	before := packagedACPSourceHashes(t, root)
	if ready, err := packagedACPScopeReady(t.Context(), root, environment.ContentRecordingMetadataOnly); err != nil || ready {
		t.Fatalf("registration alone admitted protocol: ready=%t err=%v", ready, err)
	}
	if !reflect.DeepEqual(before, packagedACPSourceHashes(t, root)) {
		t.Fatal("readiness mutated source")
	}
	publish()
	before = packagedACPSourceHashes(t, root)
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingMetadataOnly, environment.ContentRecordingFull} {
		if ready, err := packagedACPScopeReady(t.Context(), root, mode); err != nil || !ready {
			t.Fatalf("actual mirrored original scope unavailable: ready=%t err=%v", ready, err)
		}
	}
	if !reflect.DeepEqual(before, packagedACPSourceHashes(t, root)) {
		t.Fatal("ready read mutated source")
	}
}

func TestPackagedACPScopeRendezvousRefusesRetiredOrCanceledPeer(t *testing.T) {
	root := packagedACPFixture(t)
	if ready, err := packagedACPScopeReady(t.Context(), root, environment.ContentRecordingFull); err == nil || ready {
		t.Fatalf("finished scope admitted new prompt: ready=%t err=%v", ready, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitPackagedACPScope(ctx, root, environment.ContentRecordingFull); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rendezvous: %v", err)
	}
}

func TestPackagedACPPrivateReadDetectsSourceChange(t *testing.T) {
	root := packagedACPFixture(t)
	path := filepath.Join(root, "storage-layout.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	err = withPackagedACPRoles(t.Context(), root, func(_ string, _ runtimedata.StorageLayout, _, _ *sql.DB) error {
		// Change only this disposable fixture after the private copy was validated.
		return os.WriteFile(path, append(before, '\n'), 0600)
	})
	if !errors.Is(err, errPackagedACPSourceChanged) {
		t.Fatalf("source change was not classified: %v", err)
	}
}
