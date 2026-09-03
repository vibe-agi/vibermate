package systemtrust

import (
	"context"
	"os"
	"testing"

	"github.com/vibe-agi/vibermate/internal/localca"
)

func TestCoordinatorObserveReturnsTheCurrentTrustObservation(t *testing.T) {
	root := testPublicRoot(t)
	executor := newMachineExecutor(root, machineState{
		presence: ExactPresencePresent,
		decision: TrustDecisionTrusted,
	})
	coordinator, _ := newTestCoordinator(t, root, executor)
	observation, err := coordinator.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !observation.usable() ||
		observation.Presence() != ExactPresencePresent ||
		observation.TrustDecision() != TrustDecisionTrusted ||
		observation.RootDigest() != root.identity.Digest() {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestPublicRootSnapshotSourceValidatesAndCopiesPublicMaterial(t *testing.T) {
	owner, cancelOwner := context.WithCancel(context.Background())
	defer cancelOwner()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	authority, err := localca.Open(
		context.Background(),
		localca.DefaultOptions(directory, owner),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithCancel(context.Background())
		defer cancel()
		_ = authority.Shutdown(shutdownContext)
	})
	source, err := NewPublicRootSnapshotSource(
		authority.Identity(),
		authority.Certificate(),
	)
	if err != nil {
		t.Fatal(err)
	}
	root, err := source.currentPublicRoot(context.Background())
	if err != nil || !root.valid() {
		t.Fatalf("valid public material was rejected: root=%+v error=%v", root, err)
	}
	if _, err := NewPublicRootSnapshotSource(
		localca.RootIdentity{},
		authority.Certificate(),
	); err == nil {
		t.Fatal("invalid identity was accepted")
	}
	if _, err := os.Stat(authority.Certificate().Path()); err != nil {
		t.Fatal(err)
	}
}
