//go:build darwin

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/acpobservation"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/recording"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

func packagedACPFixture(t *testing.T) string {
	t.Helper()
	root, _ := packagedACPFixtureState(t, true)
	return root
}

func packagedACPFixtureState(t *testing.T, finished bool) (string, func()) {
	t.Helper()
	ctx := t.Context()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owner, err := runtimedata.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		defer owner.Release()
	} else {
		t.Cleanup(func() { owner.Release() })
	}
	host, err := instanceguard.Acquire(filepath.Join(root, "server.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		defer host.Release()
	} else {
		t.Cleanup(func() { host.Release() })
	}
	layout, err := runtimedata.PrepareStorageOwned(ctx, runtimedata.OwnedStorage{Directory: root, Runtime: owner, Host: host}, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	control, err := runtimepersistence.OpenControl(ctx, runtimepersistence.ControlOptions{DatabasePath: filepath.Join(root, layout.Control.RelativePath), LayoutID: layout.ID, BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		defer control.Shutdown(context.Background())
	} else {
		t.Cleanup(func() { control.Shutdown(context.Background()) })
	}
	digest := sha256.Sum256([]byte(layout.ID))
	observations, err := runtimepersistence.OpenObservations(ctx, runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(root, layout.Observation.RelativePath), LayoutID: layout.ID, ArchiveID: "current-" + hex.EncodeToString(digest[:16]), BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		defer observations.Shutdown(context.Background())
	} else {
		t.Cleanup(func() { observations.Shutdown(context.Background()) })
	}
	runs, err := capturerun.NewManager(ctx, capturerun.DefaultOptions(control.CaptureRunRepository()))
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		defer runs.Shutdown(context.Background())
	} else {
		t.Cleanup(func() { runs.Shutdown(context.Background()) })
	}
	for _, mode := range []environment.ContentRecordingMode{environment.ContentRecordingMetadataOnly, environment.ContentRecordingFull} {
		grant, err := runs.Create(ctx, capturerun.CreateCommand{CWD: "/fixture", CanonicalExecutablePath: "/bin/fixture", ExecutableLabel: "fixture", CatalogRevision: 1, Lifetime: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		registration, err := control.ACPRegistrations().Register(ctx, acpobservation.Registration{RunID: grant.Run.ID, Policy: environment.ContentRecordingPolicy{Mode: mode, RetentionDays: 30}, ExpiresAtMillis: time.Now().Add(time.Hour).UnixMilli()}, time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		exitCode := 0
		snapshot := acpobservation.Snapshot{Revision: 1, Final: true, ExitCode: &exitCode, Sessions: []acpobservation.Session{{ID: "session", Operation: "new"}}, Prompts: []acpobservation.Prompt{{Sequence: 1, SessionID: "session", State: "completed"}}}
		if mode == environment.ContentRecordingFull {
			snapshot.Prompts[0].UserText = "fixture question"
			snapshot.Prompts[0].AgentText = "fixture answer"
		}
		if finished {
			if err = observations.PutACP(ctx, acpobservation.Record{RunID: grant.Run.ID, Scope: recording.Scope{Capture: captureidentity.Reference{Kind: captureidentity.KindManagedRun, ID: grant.Run.ID}, Fence: registration.Fence}, Policy: registration.Policy, ExpiresAtMillis: registration.ExpiresAtMillis, Snapshot: snapshot}); err != nil {
				t.Fatal(err)
			}
		}
		if finished {
			if err = runs.Finish(ctx, grant.Run.ID, grant.ControlCapability); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = runtimedata.MarkObservationProvisioned(ctx, root, layout.ID, owner); err != nil {
		t.Fatal(err)
	}
	return root, func() {
		if err := observations.PublishInspectionMirror(ctx, control); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPackagedACPReadbackUsesDeclaredCurrentRoles(t *testing.T) {
	root := packagedACPFixture(t)
	before := packagedACPSourceHashes(t, root)
	values, err := readPackagedACPEvidence(t.Context(), root)
	if err != nil || len(values) != 2 {
		t.Fatalf("current role evidence=%d err=%v", len(values), err)
	}
	modes := map[environment.ContentRecordingMode]bool{}
	for _, v := range values {
		if modes[v.Policy.Mode] || v.State != "finished" || !v.Snapshot.Final || v.Snapshot.Incomplete || len(v.Snapshot.Prompts) != 1 || len(v.Snapshot.Sessions) != 1 || v.Snapshot.Prompts[0].State != "completed" {
			t.Fatal("incomplete ACP evidence", v)
		}
		modes[v.Policy.Mode] = true
		if v.Policy.Mode == environment.ContentRecordingFull {
			if v.Snapshot.Prompts[0].UserText != "fixture question" || v.Snapshot.Prompts[0].AgentText != "fixture answer" {
				t.Fatal("full text changed")
			}
		} else if v.Snapshot.Prompts[0].UserText != "" || v.Snapshot.Prompts[0].AgentText != "" {
			t.Fatal("metadata leaked text")
		}
	}
	if !reflect.DeepEqual(packagedACPSourceHashes(t, root), before) {
		t.Fatal("source files or sidecars changed")
	}
}

func packagedACPSourceHashes(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string][32]byte{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		values[entry.Name()] = sha256.Sum256(data)
	}
	return values
}

func TestPackagedACPReadbackRefusesContradictoryRoles(t *testing.T) {
	for _, test := range []struct{ name, database, sql string }{
		{"stale_scope", "observations.db", `UPDATE acp_snapshots SET capture_generation=capture_generation+1`},
		{"policy", "observations.db", `UPDATE acp_snapshots SET retention_days=retention_days+1`},
		{"expiry", "observations.db", `UPDATE acp_snapshots SET expires_at_unix_ms=expires_at_unix_ms+1`},
		{"revision", "observations.db", `UPDATE acp_snapshots SET revision=revision+1`},
		{"layout", "observations.db", `UPDATE observation_storage_state SET layout_id='wrong-layout'`},
		{"not_finished", "runtime.db", `UPDATE capture_runs SET state='created'`},
		{"missing_registration", "runtime.db", `DELETE FROM acp_registrations`},
		{"missing_snapshot", "observations.db", `DELETE FROM acp_snapshots`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := packagedACPFixture(t)
			db, err := sql.Open("sqlite", filepath.Join(root, test.database))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			if _, err = db.ExecContext(t.Context(), test.sql); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			before := packagedACPSourceHashes(t, root)
			if values, err := readPackagedACPEvidence(t.Context(), root); err == nil || len(values) != 0 {
				t.Fatalf("contradiction returned evidence=%d err=%v", len(values), err)
			}
			if !reflect.DeepEqual(before, packagedACPSourceHashes(t, root)) {
				t.Fatal("refusal changed source")
			}
		})
	}
	t.Run("missing_source", func(t *testing.T) {
		root := packagedACPFixture(t)
		if err := os.Remove(filepath.Join(root, "observations.db")); err != nil {
			t.Fatal(err)
		}
		before := packagedACPSourceHashes(t, root)
		if values, err := readPackagedACPEvidence(t.Context(), root); err == nil || len(values) != 0 {
			t.Fatalf("missing source returned evidence=%d err=%v", len(values), err)
		}
		if !reflect.DeepEqual(before, packagedACPSourceHashes(t, root)) {
			t.Fatal("missing source was recreated")
		}
	})
}

func TestPackagedACPReadbackIncludesRetainedWALWithoutSourceSHM(t *testing.T) {
	root := packagedACPFixture(t)
	writer, err := sql.Open("sqlite", filepath.Join(root, "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err = writer.ExecContext(t.Context(), `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE acp_snapshots SET snapshot=CAST(json_set(snapshot,'$.agent.name','wal-only-marker') AS BLOB)`); err != nil {
		t.Fatal(err)
	}
	wal, err := os.Stat(filepath.Join(root, "observations.db-wal"))
	if err != nil || wal.Size() == 0 {
		t.Fatal("committed WAL fixture absent", err)
	}
	// Copy the quiescent committed fixture before writer close checkpoints it.
	// The stopped source handed to the reader has no SHM to borrow.
	retained := t.TempDir()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "-shm") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(retained, entry.Name()), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := packagedACPSourceHashes(t, retained)
	values, err := readPackagedACPEvidence(t.Context(), retained)
	if err != nil || len(values) != 2 {
		t.Fatalf("WAL evidence=%d err=%v", len(values), err)
	}
	for _, v := range values {
		if v.Snapshot.Agent.Name != "wal-only-marker" {
			t.Fatal("committed WAL ignored")
		}
	}
	if !reflect.DeepEqual(before, packagedACPSourceHashes(t, retained)) {
		t.Fatal("read created SHM or changed retained DB/WAL")
	}
}
