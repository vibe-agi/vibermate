package productruntime

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/connectionevent"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/recording"
)

func retainedMaintenanceArchive(t *testing.T) (*Runtime, string) {
	t.Helper()
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	root := options.Paths.DataDirectory()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("../runtimepersistence/schema/v1.sql")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',1,'2026-10-03T00:00:00Z');
INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES('retained-legacy',randomblob(32),randomblob(32),'/synthetic-maintenance','/bin/tool','tool',1,'finished',10,20,20);
INSERT INTO acp_observations(run_id,policy,revision,snapshot,final,expires_at_unix_ms) VALUES('retained-legacy','{"mode":"full","retentionDays":7}',1,zeroblob(1048576),1,9999999999999)`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	r := startTestRuntime(t, options)
	t.Cleanup(func() { shutdownRuntime(t, r) })
	waitRecording(t, r)
	return r, filepath.Join(root, r.recordingStorage.layout.Archives[0].RelativeDirectory, "runtime.db")
}

func maintenanceArchiveCookie(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var cookie int
	if err = db.QueryRow(`PRAGMA schema_version`).Scan(&cookie); err != nil {
		t.Fatal(err)
	}
	return cookie
}

// Publishing unconditional intent would advance the generation, invalidate
// inspection and rewrite a retained archive despite there being no work.
func TestRoutineRecordingNoWorkKeepsArchiveAndGeneration(t *testing.T) {
	r, archive := retainedMaintenanceArchive(t)
	before, err := r.storage.RecordingCleanupState(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cookie := maintenanceArchiveCookie(t, archive)
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	for pass := range 2 {
		if err = r.recordingStorage.requestRoutineRecordingCleanup(t.Context(), r.clock.Now().UTC().Add(time.Duration(pass+2)*time.Minute)); err != nil {
			t.Fatal(err)
		}
		waitOwnedRecordingCleanup(t, r)
		after, err := r.storage.RecordingCleanupState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Errorf("no-work routine pass changed durable cleanup state: before=%+v after=%+v", before, after)
		}
		if got := maintenanceArchiveCookie(t, archive); got != cookie {
			t.Errorf("no-work archive VACUUM: cookie %d -> %d", cookie, got)
		}
		got, err := os.ReadFile(archive)
		if err != nil || !bytes.Equal(got, data) {
			t.Errorf("no-work archive bytes changed: %v", err)
		}
	}
}

// Current-source deletion must physically remove its bytes without VACUUMing
// an unrelated frozen source. Display release counts cannot identify this.
func TestRecordingCleanupCompactsOnlyMutatedFreshSources(t *testing.T) {
	r, archive := retainedMaintenanceArchive(t)
	cookie := maintenanceArchiveCookie(t, archive)
	grant, err := r.ManualCaptures().Create(t.Context(), manualcapture.CreateCommand{Owner: manualcapture.NewLocalOwnerScope(), DisplayName: "current deletion", ClientClass: manualcapture.ClientCLI, Lifetime: manualcapture.LifetimeUntilRevoked})
	if err != nil {
		t.Fatal(err)
	}
	ref := captureidentity.Reference{Kind: captureidentity.KindManualCapture, ID: string(grant.Capture.ID)}
	event := connectionevent.Event{ConnectionID: "current-only-deletion", SourceConfidence: connectionevent.SourceConfidenceUnknown, RequestedHost: "private-current-cleanup.example.test", Port: 443, Decryption: connectionevent.DecryptionNone, Phase: connectionevent.PhaseAttempted, StartedAt: time.Now().UTC()}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = r.recordingStorage.PutConnection(t.Context(), recording.Scope{Capture: ref, Fence: recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}}, event)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capture mirror never became available", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = r.recordingStorage.DeleteCapture(t.Context(), string(ref.Kind), ref.ID); err != nil {
		t.Fatal(err)
	}
	waitOwnedRecordingCleanup(t, r)
	if got := maintenanceArchiveCookie(t, archive); got != cookie {
		t.Errorf("untouched source was VACUUMed: %d -> %d", cookie, got)
	}
	data, err := os.ReadFile(filepath.Join(r.paths.DataDirectory(), "observations.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(event.RequestedHost)) {
		t.Fatal("completed deletion retained physical current-source bytes")
	}
}

// ACP rows are deliberately absent from the display Released counters. Their
// expiry still owns physical purge and cannot be mistaken for a no-work pass.
func TestRoutineRecordingExpiryPurgesUnreportedArchivePayload(t *testing.T) {
	r, archive := retainedMaintenanceArchive(t)
	before, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	cookie := maintenanceArchiveCookie(t, archive)
	if err = r.recordingStorage.requestRoutineRecordingCleanup(t.Context(), time.UnixMilli(9999999999999)); err != nil {
		t.Fatal(err)
	}
	waitOwnedRecordingCleanup(t, r)
	after, err := os.Stat(archive)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size()-512*1024 {
		t.Fatalf("expired ACP payload bytes not released: %d -> %d", before.Size(), after.Size())
	}
	if got := maintenanceArchiveCookie(t, archive); got <= cookie {
		t.Fatalf("mutated archive was not compacted: %d -> %d", cookie, got)
	}
}

func maintenanceFailureProjection(t *testing.T, status RuntimeStatus) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	result, ok := value["recordingMaintenanceFailure"].(map[string]any)
	if !ok {
		t.Fatalf("maintenance failure missing from status: %s", encoded)
	}
	return result
}
