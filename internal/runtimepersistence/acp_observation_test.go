package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

func TestACPAdditiveUpgradePreservesReleasedBaselineAndSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	// Build precisely the released base schema, without ACP extension tables.
	base := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	digest, err := initializeSchema(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if err = base.Close(); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, path)
	state, err := store.SchemaStateReader().ReadSchemaState(ctx)
	if err != nil || state.SourceSHA256 != digest {
		t.Fatalf("released baseline changed: %v", err)
	}
	manager, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(store.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	grant, err := manager.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "test-agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	initial := acpobservation.NewObserver(false).Snapshot()
	record := acpobservation.Record{RunID: grant.Run.ID, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 30}, ExpiresAtMillis: now + 10000, Snapshot: initial}
	if err := store.ACPObservations().Create(ctx, record, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ACPObservations().Create(ctx, record, now); err != nil {
		t.Fatalf("create retry: %v", err)
	}
	observer := acpobservation.NewObserver(false)
	observer.Finish(0, false)
	final := observer.Snapshot()
	if err := store.ACPObservations().Save(ctx, record.RunID, final, now); err != nil {
		t.Fatal(err)
	}
	if err := store.ACPObservations().Save(ctx, record.RunID, final, now); err != nil {
		t.Fatalf("save retry: %v", err)
	}
	if err := store.ACPObservations().Save(ctx, record.RunID, initial, now); !errors.Is(err, acpobservation.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, path)
	defer shutdownTestStore(t, reopened)
	got, err := reopened.ACPObservations().Read(ctx, record.RunID, now)
	if err != nil || !got.Snapshot.Final || got.Snapshot.Revision != final.Revision {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	got, err = reopened.ACPObservations().Read(ctx, record.RunID, now+20000)
	if err != nil || !got.Expired || len(got.Snapshot.Prompts) != 0 {
		t.Fatalf("expiry: %+v %v", got, err)
	}
	// Deleting the parent cannot leave ACP content orphaned.
	if _, err := reopened.database.ExecContext(ctx, `DELETE FROM capture_runs WHERE run_id=?`, record.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ACPObservations().Read(ctx, record.RunID, now); !errors.Is(err, acpobservation.ErrNotFound) {
		t.Fatalf("orphan: %v", err)
	}
}

func TestACPRevokedRuntimeIdentityCannotPublishThroughALiveRun(t *testing.T) {
	for _, action := range []string{"logout", "disable"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
			defer shutdownTestStore(t, store)
			clock := runtimeUserTestClock{now: time.Now().UTC()}
			users, err := runtimeuser.New(runtimeuser.Options{Repository: store.RuntimeUserRepository(), Clock: clock, Random: rand.Reader, SessionLifetime: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			user, err := users.Create(ctx, runtimeuser.CreateCommand{Username: "acp-user", Password: []byte("test-only-password")})
			if err != nil {
				t.Fatal(err)
			}
			machine, err := workspaceidentity.ParseMachineID(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
			if err != nil {
				t.Fatal(err)
			}
			login, err := users.Login(ctx, runtimeuser.LoginCommand{Username: "acp-user", Password: []byte("test-only-password"), MachineID: machine, DeviceName: "test editor"})
			if err != nil {
				t.Fatal(err)
			}
			runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(store.CaptureRunRepository()))
			if err != nil {
				t.Fatal(err)
			}
			defer runs.Shutdown(ctx)
			grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "test-agent", CatalogRevision: 1, Lifetime: time.Minute, RuntimeUserID: user.ID, RuntimeUsername: user.Username, LoginSessionID: login.ID, DeviceName: "test editor"})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UnixMilli()
			initial := acpobservation.NewObserver(false).Snapshot()
			record := acpobservation.Record{RunID: grant.Run.ID, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 30}, ExpiresAtMillis: now + 10000, Snapshot: initial}
			if err := store.ACPObservations().Create(ctx, record, now); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.AuthorizeControl(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
				t.Fatal(err)
			}
			if action == "logout" {
				err = users.Logout(ctx, login.Token.Value())
			} else {
				_, err = users.Disable(ctx, user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runs.AuthorizeControl(ctx, grant.Run.ID, grant.ControlCapability); !errors.Is(err, capturerun.ErrCapabilityRejected) {
				t.Fatalf("revoked authority accepted: %v", err)
			}
			initial.Revision++
			if err := store.ACPObservations().Save(ctx, grant.Run.ID, initial, now); !errors.Is(err, acpobservation.ErrNotFound) {
				t.Fatalf("revoked identity published: %v", err)
			}
		})
	}
}

func TestACPUnfamiliarSchemaIsNotSilentlyReset(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	if _, err := store.database.ExecContext(ctx, `UPDATE acp_schema_metadata SET source_sha256='unfamiliar-test-digest'`); err != nil {
		t.Fatal(err)
	}
	shutdownTestStore(t, store)
	if reopened, err := Open(ctx, Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout}); err == nil {
		shutdownTestStore(t, reopened)
		t.Fatal("unfamiliar extension opened")
	}
	database := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
	defer database.Close()
	var digest string
	if err := database.QueryRowContext(ctx, `SELECT source_sha256 FROM acp_schema_metadata`).Scan(&digest); err != nil || digest != "unfamiliar-test-digest" {
		t.Fatal("failed open rewrote extension")
	}
}

func TestACPRegistrationRechecksUnattachedParentInsideTransaction(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(store.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Shutdown(ctx)
	grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/workspace", CanonicalExecutablePath: "/bin/test-agent", ExecutableLabel: "test-agent", CatalogRevision: 1, Lifetime: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	// The control layer can authorize while Created, then attachment can win
	// before the storage transaction begins. Recheck the phase atomically.
	if _, err := runs.AuthorizeControl(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Attach(ctx, grant.Run.ID, grant.ControlCapability, 744); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	record := acpobservation.Record{RunID: grant.Run.ID, Policy: environment.ContentRecordingPolicy{Mode: environment.ContentRecordingMetadataOnly, RetentionDays: 30}, ExpiresAtMillis: now + 10000, Snapshot: acpobservation.NewObserver(false).Snapshot()}
	if err := store.ACPObservations().Create(ctx, record, now); !errors.Is(err, acpobservation.ErrConflict) {
		t.Fatalf("attached HTTP run was relabeled ACP: %v", err)
	}
	if _, err := store.ACPObservations().Read(ctx, grant.Run.ID, now); !errors.Is(err, acpobservation.ErrNotFound) {
		t.Fatalf("rejected registration left an ACP record: %v", err)
	}
}
