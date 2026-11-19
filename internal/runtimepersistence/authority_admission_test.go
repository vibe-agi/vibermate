package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureauthority"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
)

func newAdmissionTestRuns(t *testing.T, store *Store) *capturerun.Manager {
	t.Helper()
	options := capturerun.DefaultOptions(store.CaptureRunRepository())
	options.Clock = &manualCaptureClock{now: time.Now().UTC()}
	runs, err := capturerun.NewManager(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Shutdown(context.Background()) })
	return runs
}

// Opening a connection reads whether its credential is still good. When that
// read fails the agent must be told to retry, not that its credential was
// refused; a lookup that completed and matched nothing stays a refusal.
func TestConnectionAdmissionTellsAStorageFailureFromARejection(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	runs := newAdmissionTestRuns(t, store)
	run, err := runs.Create(t.Context(), capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "codex", CatalogRevision: 1, Lifetime: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	clock := &manualCaptureClock{now: time.Now().UTC()}
	manual := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	defer func() { _ = manual.Shutdown(context.Background()) }()
	owner := manualcapture.NewLocalOwnerScope()
	grant, err := manual.Create(t.Context(), manualcapture.CreateCommand{Owner: owner, DisplayName: "admission", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := manualcapture.ParseID(grant.Capture.ID)
	rotated, err := manual.Rotate(t.Context(), manualcapture.RotateCommand{Owner: owner, ID: id, ExpectedCredentialRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AuthorizeProxy(t.Context(), run.ProxyCapability); err != nil {
		t.Fatalf("an active run was refused: %v", err)
	}
	if _, err := manual.AuthorizeProxy(t.Context(), rotated.Credential); err != nil {
		t.Fatalf("an active manual capture was refused: %v", err)
	}
	_, err = manual.AuthorizeProxy(t.Context(), grant.Credential)
	if !errors.Is(err, manualcapture.ErrCredentialRejected) || errors.Is(err, captureauthority.ErrUnavailable) {
		t.Fatalf("a rotated-out credential must be a plain rejection: %v", err)
	}
	// The tables disappearing stand in for any storage failure.
	for _, table := range []string{"capture_runs", "manual_captures"} {
		if _, err := store.database.ExecContext(t.Context(), `ALTER TABLE `+table+` RENAME TO `+table+`_gone`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runs.AuthorizeProxy(t.Context(), run.ProxyCapability); !errors.Is(err, captureauthority.ErrUnavailable) {
		t.Fatalf("a failed run lookup must be reported as unavailable: %v", err)
	}
	if _, err := manual.AuthorizeProxy(t.Context(), rotated.Credential); !errors.Is(err, captureauthority.ErrUnavailable) {
		t.Fatalf("a failed manual capture lookup must be reported as unavailable: %v", err)
	}
}

// A long write must not hold up an agent opening a connection: the authority
// reads use their own read-only connections, and admission writes nothing.
func TestConnectionAdmissionDoesNotWaitForTheWriter(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	runs := newAdmissionTestRuns(t, store)
	run, err := runs.Create(t.Context(), capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "codex", CatalogRevision: 1, Lifetime: 10 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	clock := &manualCaptureClock{now: time.Now().UTC()}
	manual := newManualCaptureManager(t, store, clock, &manualCaptureRandom{})
	defer func() { _ = manual.Shutdown(context.Background()) }()
	owner := manualcapture.NewLocalOwnerScope()
	temporary, err := manual.Create(t.Context(), manualcapture.CreateCommand{Owner: owner, DisplayName: "temporary", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeTemporary, ExpiresIn: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// The single writer connection is held for the whole check.
	held, err := store.database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	if _, err := held.ExecContext(t.Context(), `UPDATE runtime_storage_state SET visibility_revision=visibility_revision WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if _, err := runs.AuthorizeProxy(ctx, run.ProxyCapability); err != nil {
		t.Fatalf("run admission waited for the writer: %v", err)
	}
	if _, err := runs.ProxyValidity(ctx, run.ProxyCapability); err != nil {
		t.Fatalf("run validity waited for the writer: %v", err)
	}
	if _, err := manual.AuthorizeProxy(ctx, temporary.Credential); err != nil {
		t.Fatalf("manual admission waited for the writer: %v", err)
	}
	if _, err := manual.ProxyValidity(ctx, temporary.Credential); err != nil {
		t.Fatalf("manual validity waited for the writer: %v", err)
	}
	// An expired temporary capture is still refused while the writer is held.
	clock.Set(clock.Now().Add(2 * time.Hour))
	if _, err := manual.AuthorizeProxy(ctx, temporary.Credential); !errors.Is(err, manualcapture.ErrCredentialRejected) || errors.Is(err, captureauthority.ErrUnavailable) {
		t.Fatalf("expired capture admitted under a held writer: %v", err)
	}
}
