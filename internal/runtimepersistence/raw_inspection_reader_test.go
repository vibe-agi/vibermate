package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/recording"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

type inspectionHeaderSource struct {
	value  string
	lookup providerauth.HeaderLookup
}

func (s *inspectionHeaderSource) ReadOverwriteHeader(_ context.Context, q providerauth.HeaderLookup) (string, error) {
	s.lookup = q
	return s.value, nil
}
func rawInspectionFixture(t *testing.T, retainedWAL ...bool) (*RawInspectionReader, *ObservationStore, *ControlStore, rawevidence.StoredEnvelope, string, []byte) {
	t.Helper()
	ctx := context.Background()
	options, current := inspectionSourcesFixture(t)
	source, db := extractionFixture(t, 2)
	walOnly := len(retainedWAL) > 0 && retainedWAL[0]
	if walOnly {
		nativeExec(t, db, `PRAGMA journal_mode=WAL;PRAGMA wal_autocheckpoint=0`)
	}
	salt := bytes.Repeat([]byte{0x3a}, 32)
	secret := "synthetic-original-overwrite"
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(secret))
	metadata, err := json.Marshal(map[string]any{"version": 1, "headers": []rawevidence.HeaderField{{Name: "User-Agent", Redacted: []rawevidence.RedactedValue{{Digest: hex.EncodeToString(mac.Sum(nil)), Bytes: len(secret)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	record := rawEvidenceRecordForTest("legacy-envelope", 1, rawevidence.LayerProviderEgress, []byte("original legacy bytes"), metadata)
	record.ObservedAt = time.Now().UTC().Truncate(time.Millisecond)
	record.ExpiresAt = record.ObservedAt.Add(24 * time.Hour)
	record.HeaderCount = 1
	record.RedactedCredentialFields = []string{"User-Agent"}
	record.AccountID = "account"
	record.AccountRevision = 2
	record.CredentialEpoch = 3
	record.UpstreamEndpointID = "endpoint"
	record.UpstreamEndpointRevision = 4
	if err = newRawEvidenceRepository(db, newOperationGate()).AppendBatch(ctx, []rawevidence.StoredEnvelope{record}, record.ObservedAt); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, db, `INSERT INTO runtime_raw_evidence_redaction(singleton,salt) VALUES(1,?)`, salt)
	var original, wal []byte
	if walOnly {
		original, err = os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		wal, err = os.ReadFile(source + "-wal")
		if err != nil || len(wal) == 0 {
			t.Fatal("fixture lacks committed WAL", err)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if !walOnly {
		original, err = os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
	}
	archivePath := filepath.Join(options.Root, "archives/legacy/runtime.db")
	if err = os.WriteFile(archivePath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if walOnly {
		if err = os.WriteFile(archivePath+"-wal", wal, 0600); err != nil {
			t.Fatal(err)
		}
	}

	nativeExec(t, current.store.database, `INSERT INTO runtime_raw_evidence_redaction(singleton,salt) VALUES(1,?)`, bytes.Repeat([]byte{0x7f}, 32))
	control := inspectionControlFixture(t)
	nativeExec(t, control.store.database, `INSERT INTO recording_capture_fences VALUES('managed_run','capture-raw-1',1,0)`)
	seedCompleteInspectionMirror(t, current, control)
	catalog, err := NewInspectionCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close(ctx) })
	return NewRawInspectionReader(catalog, control, current, nil), current, control, record, archivePath, original
}
func TestRawInspectionLegacyRevealUsesOriginalSaltAndCurrentQualifiedAudit(t *testing.T) {
	r, current, _, record, path, original := rawInspectionFixture(t)
	ctx := context.Background()
	list, err := r.ListExchange(ctx, record.ExchangeID)
	if err != nil || len(list) != 1 || list[0].EnvelopeID != record.EnvelopeID {
		t.Fatalf("legacy descriptors: %v", err)
	}
	request := rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "inspection-user"}
	revealed, err := r.Reveal(ctx, request)
	if err != nil || !bytes.Equal(revealed.Payload.Body, record.Body) {
		t.Fatalf("legacy body reveal: %v", err)
	}
	if len(revealed.Payload.Headers) != 1 || len(revealed.Payload.Headers[0].Values) != 0 {
		t.Fatal("ordinary reveal disclosed credential")
	}
	source := &inspectionHeaderSource{value: "synthetic-original-overwrite"}
	header, err := r.RevealHeader(ctx, rawevidence.HeaderRevealRequest{RevealRequest: request, Name: "user-agent"}, source)
	if err != nil || header.Value != source.value || source.lookup.CredentialEpoch != 3 || source.lookup.AccountRevision != 2 || source.lookup.UpstreamEndpointRevision != 4 {
		t.Fatalf("original-salt header proof unavailable: %v", err)
	}
	var count int
	if err = current.store.database.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits WHERE source_id='legacy' AND source_generation=1 AND recording_scope_kind='managed_run' AND recording_scope_id='capture-raw-1' AND recording_archive_epoch=1 AND recording_capture_generation=1 AND envelope_id=? AND exchange_id=? AND actor_id='inspection-user' AND outcome='succeeded'`, record.EnvelopeID, record.ExchangeID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("source-qualified current audit: count=%d error=%v", count, err)
	}
	if err = current.store.database.QueryRow(`SELECT count(*) FROM runtime_raw_evidence_envelopes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("legacy body copied to satisfy local audit FK")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("inspection mutated original archive")
	}
}

type locatedAuditFunc func(context.Context, rawevidence.LocatedRevealAudit) error

func (f locatedAuditFunc) AppendLocatedRevealAudit(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
	return f(ctx, a)
}
func TestRawInspectionRequiresMatchingMirrorAndKnownOriginalScope(t *testing.T) {
	for _, state := range []string{"missing", "partial", "stale", "unknown_current"} {
		t.Run(state, func(t *testing.T) {
			r, current, _, record, _, _ := rawInspectionFixture(t)
			ctx := context.Background()
			switch state {
			case "missing":
				nativeExec(t, current.store.database, `DELETE FROM inspection_mirror_active`)
			case "partial":
				nativeExec(t, current.store.database, `UPDATE inspection_mirror_generations SET complete=0`)
			case "stale":
				nativeExec(t, current.store.database, `UPDATE inspection_mirror_generations SET visibility_revision=visibility_revision+1`)
			case "unknown_current":
				record.EnvelopeID = "unknown-original"
				tx, err := current.store.database.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				digest, err := storeEvidenceBody(ctx, tx, record.Body)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(`INSERT INTO runtime_raw_evidence_envelopes (`+rawEvidenceColumns+`) VALUES (`+rawEvidencePlaceholders+`)`, rawEvidenceArguments(record, digest)...); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
			}
			items, err := r.ListExchange(ctx, record.ExchangeID)
			if state != "unknown_current" {
				if !errors.Is(err, ErrInspectionUnavailable) {
					t.Fatalf("%s mirror accepted: %v", state, err)
				}
				return
			}
			if err != nil || len(items) != 1 || items[0].EnvelopeID == record.EnvelopeID {
				t.Fatalf("unknown original scope visible: %v", err)
			}
			if _, err = r.Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"}); !errors.Is(err, rawevidence.ErrEnvelopeNotFound) {
				t.Fatalf("unknown scope revealed: %v", err)
			}
		})
	}
}
func TestRawInspectionAuditReleasesSQLButRetainsOwnershipAndRechecks(t *testing.T) {
	r, current, control, record, _, _ := rawInspectionFixture(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	r.audit = locatedAuditFunc(func(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
		if err := current.AppendLocatedRevealAudit(ctx, a); err != nil {
			return err
		}
		var busy, frames, checkpointed int
		if err := current.store.database.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil {
			return err
		}
		if busy != 0 {
			return errors.New("history SQL still pinned during audit")
		}
		close(entered)
		<-release
		return nil
	})
	results := make(chan error, 1)
	go func() {
		value, err := r.Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"})
		if len(value.Payload.Body) != 0 {
			results <- errors.New("body disclosed across deletion")
			return
		}
		results <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("audit did not release history transaction")
	}
	nativeExec(t, control.store.database, `UPDATE recording_capture_fences SET deleted=1,generation=generation+1;UPDATE runtime_storage_state SET visibility_revision=visibility_revision+1`)
	r.catalog.mu.Lock()
	active := r.catalog.active
	r.catalog.mu.Unlock()
	if active != 1 {
		t.Error("audit lost bounded physical read ownership")
	}
	close(release)
	if err := <-results; !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("post-audit visibility not rechecked: %v", err)
	}
}
func TestRawInspectionUnavailableAuditNeverDisclosesAndDoesNotAffectList(t *testing.T) {
	r, _, _, record, _, _ := rawInspectionFixture(t)
	ctx := context.Background()
	r.audit = locatedAuditFunc(func(context.Context, rawevidence.LocatedRevealAudit) error {
		return errors.New("optional audit unavailable")
	})
	request := rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"}
	result, err := r.Reveal(ctx, request)
	if err == nil || len(result.Payload.Body) != 0 {
		t.Fatal("body returned without durable audit")
	}
	header, err := r.RevealHeader(ctx, rawevidence.HeaderRevealRequest{RevealRequest: request, Name: "user-agent"}, &inspectionHeaderSource{value: "synthetic-original-overwrite"})
	if err == nil || header.Value != "" {
		t.Fatal("header returned without durable audit")
	}
	if list, err := r.ListExchange(ctx, record.ExchangeID); err != nil || len(list) != 1 {
		t.Fatalf("audit failure disabled ordinary inspection: %v", err)
	}
}
func TestRawInspectionDeduplicatesImmutableIdentityBeforeExchangeFilter(t *testing.T) {
	r, current, _, record, _, _ := rawInspectionFixture(t)
	ctx := context.Background()
	nativeExec(t, current.store.database, `UPDATE runtime_raw_evidence_redaction SET salt=?`, bytes.Repeat([]byte{0x3a}, 32))
	record.RecordingFence = recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}
	if err := current.RawEvidenceRepository().AppendBatch(ctx, []rawevidence.StoredEnvelope{record}, time.Now()); err != nil {
		t.Fatal(err)
	}
	list, err := r.ListExchange(ctx, record.ExchangeID)
	if err != nil || len(list) != 1 {
		t.Fatalf("duplicate identity counted twice: %v", err)
	}
	nativeExec(t, current.store.database, `UPDATE runtime_raw_evidence_envelopes SET exchange_id='other-exchange' WHERE envelope_id=?`, record.EnvelopeID)
	if _, err = r.ListExchange(ctx, record.ExchangeID); !errors.Is(err, ErrInspectionUnavailable) {
		t.Fatalf("conflicting later identity exposed obsolete archive: %v", err)
	}
}

func TestRawInspectionMalformedBodyRemainsListableButAuditsUnavailable(t *testing.T) {
	r, current, _, record, path, _ := rawInspectionFixture(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE runtime_evidence_bodies SET plain_bytes=999`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if list, err := r.ListExchange(ctx, record.ExchangeID); err != nil || len(list) != 1 {
		t.Fatalf("descriptor read loaded corrupt body: %v", err)
	}
	request := rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"}
	if value, err := r.Reveal(ctx, request); err == nil || len(value.Payload.Body) != 0 {
		t.Fatal("corrupt body disclosed")
	}
	source := &inspectionHeaderSource{value: "synthetic-original-overwrite"}
	if value, err := r.RevealHeader(ctx, rawevidence.HeaderRevealRequest{RevealRequest: request, Name: "user-agent"}, source); err == nil || value.Value != "" || source.lookup.AccountID != "" {
		t.Fatal("corrupt source reached credential lookup")
	}
	var count int
	if err = current.store.database.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits WHERE outcome='unavailable'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("unavailable attempts were not audited", err)
	}
}
func TestRawInspectionNeverCreatesMissingLegacySalt(t *testing.T) {
	r, current, _, record, path, _ := rawInspectionFixture(t)
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM runtime_raw_evidence_redaction`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := &inspectionHeaderSource{value: "synthetic-original-overwrite"}
	q := rawevidence.HeaderRevealRequest{RevealRequest: rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"}, Name: "User-Agent"}
	if value, err := r.RevealHeader(ctx, q, source); err == nil || value.Value != "" || source.lookup.AccountID != "" {
		t.Fatal("missing original salt replaced or ignored")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("missing salt was initialized in archive")
	}
	var count int
	if err = current.store.database.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits WHERE outcome='unavailable'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing-salt refusal was not audited", err)
	}
}
func TestRawInspectionAuditCleanupUsesScopeWithoutLocalEnvelope(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	for _, id := range []string{"orphan-a", "orphan-b"} {
		nativeExec(t, store.database, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES(?,randomblob(32),randomblob(32),'/tmp','/bin/echo','fixture',1,'finished',1,2,2)`, id)
		nativeExec(t, store.database, `INSERT INTO runtime_raw_inspection_reveal_audits(layout_id,source_set,source_generation,source_id,recording_scope_kind,recording_scope_id,recording_archive_epoch,recording_capture_generation,visibility_revision,usage_policy_revision,envelope_id,exchange_id,actor_id,outcome,occurred_at_unix_ms) VALUES('transitional-monolith',?,1,'legacy','managed_run',?,1,1,1,1,?,'legacy-exchange','owner','succeeded',1)`, strings.Repeat("a", 64), id, "absent-"+id)
	}
	if _, err := store.DeleteCapture(context.Background(), "managed_run", "orphan-a"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.database.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits WHERE recording_scope_id='orphan-a'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("source audit escaped scoped cleanup", err)
	}
	if err := store.database.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits WHERE recording_scope_id='orphan-b'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("other Capture audit was erased", err)
	}
	if _, err := store.ClearEvidence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.database.QueryRow(`SELECT count(*) FROM runtime_raw_inspection_reveal_audits`).Scan(&count); err != nil || count != 0 {
		t.Fatal("clear retained source-qualified audits", err)
	}
}

func TestRawInspectionReportsOnlyCurrentWriterHealth(t *testing.T) {
	r, current, _, _, _, _ := rawInspectionFixture(t)
	ctx := context.Background()
	manager, err := rawevidence.Open(ctx, rawevidence.Options{Repository: current.RawEvidenceRepository(), Random: rand.Reader, Clock: rawevidence.SystemClock{}, Config: rawevidence.DefaultConfig()})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Shutdown(ctx)
	r.health = manager
	status, ok := any(r).(interface {
		Statistics() rawevidence.Statistics
		WriterStatisticsAvailable() bool
	})
	if !ok {
		t.Fatal("independent reader lost current writer health port")
	}
	if !status.WriterStatisticsAvailable() || status.Statistics().WriterID != manager.Statistics().WriterID || r.Recovery() != manager.Recovery() {
		t.Fatal("archive data substituted for current writer health")
	}
	r.health = nil
	if status.WriterStatisticsAvailable() {
		t.Fatal("missing current writer reported active")
	}
}

func TestRawInspectionRetainedWALContainsOnlyCopyOfEnvelopeAndSalt(t *testing.T) {
	r, _, _, record, path, main := rawInspectionFixture(t, true)
	ctx := context.Background()
	wal, err := os.ReadFile(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(t.TempDir(), "base.db")
	if err = os.WriteFile(basePath, main, 0600); err != nil {
		t.Fatal(err)
	}
	base, err := sql.Open("sqlite", "file:"+basePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	var count int
	if err = base.QueryRow(`SELECT count(*) FROM runtime_raw_evidence_envelopes`).Scan(&count); err != nil || count != 0 {
		t.Fatal("fixture envelope is not WAL-only", err)
	}
	q := rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"}
	if value, err := r.Reveal(ctx, q); err != nil || !bytes.Equal(value.Payload.Body, record.Body) {
		t.Fatalf("committed WAL body lost: %v", err)
	}
	source := &inspectionHeaderSource{value: "synthetic-original-overwrite"}
	if value, err := r.RevealHeader(ctx, rawevidence.HeaderRevealRequest{RevealRequest: q, Name: "User-Agent"}, source); err != nil || value.Value != source.value {
		t.Fatalf("WAL-only original salt lost: %v", err)
	}
	afterMain, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(afterMain, main) {
		t.Fatal("read checkpointed original main", err)
	}
	afterWAL, err := os.ReadFile(path + "-wal")
	if err != nil || !bytes.Equal(afterWAL, wal) {
		t.Fatal("read mutated committed archive WAL", err)
	}
}
func TestRawInspectionListLimitFailsWithoutPartialResults(t *testing.T) {
	r, current, _, record, _, _ := rawInspectionFixture(t)
	ctx := context.Background()
	record.Body = nil
	record.BodyBytes = 0
	record.BodySHA256 = sha256.Sum256(nil)
	record.HeaderCount = 0
	record.RedactedCredentialFields = nil
	record.PayloadMetadata = []byte(`{"version":1,"headers":[]}`)
	record.RecordingFence = recording.Fence{ArchiveEpoch: 1, CaptureGeneration: 1}
	record.WriterID = "bounded-writer"
	records := make([]rawevidence.StoredEnvelope, rawInspectionMaximumRows+1)
	for i := range records {
		records[i] = record
		records[i].EnvelopeID = fmt.Sprintf("bounded-%d", i)
		records[i].Watermark = uint64(i + 1)
	}
	if err := current.RawEvidenceRepository().AppendBatch(ctx, records, time.Now()); err != nil {
		t.Fatal(err)
	}
	items, err := r.ListExchange(ctx, record.ExchangeID)
	if !errors.Is(err, ErrInspectionSourceLimit) || len(items) != 0 {
		t.Fatalf("oversized inspection reported partial success: count=%d error=%v", len(items), err)
	}
}

func TestRawInspectionExpiryDuringAuditWithholdsBodyAndHeader(t *testing.T) {
	for _, header := range []bool{false, true} {
		t.Run(fmt.Sprint(header), func(t *testing.T) {
			r, current, _, record, _, _ := rawInspectionFixture(t)
			ctx := context.Background()
			var now atomic.Int64
			now.Store(record.ExpiresAt.UnixMilli() - 1)
			r.now = func() time.Time { return time.UnixMilli(now.Load()).UTC() }
			r.audit = locatedAuditFunc(func(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
				err := current.AppendLocatedRevealAudit(ctx, a)
				now.Store(record.ExpiresAt.UnixMilli() + 1)
				return err
			})
			q := rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"}
			if header {
				value, err := r.RevealHeader(ctx, rawevidence.HeaderRevealRequest{RevealRequest: q, Name: "User-Agent"}, &inspectionHeaderSource{value: "synthetic-original-overwrite"})
				if err == nil || value.Value != "" {
					t.Fatal("header disclosed after expiry during audit")
				}
			} else {
				value, err := r.Reveal(ctx, q)
				if err == nil || len(value.Payload.Body) != 0 {
					t.Fatal("body disclosed after expiry during audit")
				}
			}
		})
	}
}

func TestRawInspectionCanceledAuditRetainsOwnerUntilActualReturn(t *testing.T) {
	r, current, _, record, _, _ := rawInspectionFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	r.audit = locatedAuditFunc(func(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
		close(entered)
		<-release
		return current.AppendLocatedRevealAudit(ctx, a)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		value, err := r.Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"})
		if len(value.Payload.Body) != 0 {
			err = errors.New("canceled audit disclosed body")
		}
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("audit not entered")
	}
	cancel()
	deadline, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	if err := r.catalog.Close(deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("catalog released physical owner before audit returned: %v", err)
	}
	select {
	case <-result:
		t.Fatal("reveal returned while external I/O still owned bytes")
	default:
	}
	r.catalog.mu.Lock()
	active := r.catalog.active
	r.catalog.mu.Unlock()
	if active != 1 {
		t.Fatal("canceled I/O released its inspection slot")
	}
	unblock()
	if err := <-result; err == nil {
		t.Fatal("canceled audit reported success")
	}
	drained, finish := context.WithTimeout(context.Background(), time.Second)
	defer finish()
	if err := r.catalog.Close(drained); err != nil {
		t.Fatal(err)
	}
}
func TestRawInspectionSourceGenerationChangesWhileAuditOwnsLease(t *testing.T) {
	r, current, _, record, _, _ := rawInspectionFixture(t)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	r.audit = locatedAuditFunc(func(ctx context.Context, a rawevidence.LocatedRevealAudit) error {
		if err := current.AppendLocatedRevealAudit(ctx, a); err != nil {
			return err
		}
		close(entered)
		<-release
		return nil
	})
	results := make(chan error, 1)
	go func() {
		value, err := r.Reveal(ctx, rawevidence.RevealRequest{EnvelopeID: record.EnvelopeID, ActorID: "owner"})
		if len(value.Payload.Body) != 0 {
			err = errors.New("old source generation disclosed body")
		}
		results <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("audit not entered")
	}
	r.catalog.mu.Lock()
	changed := r.catalog.changed
	r.catalog.mu.Unlock()
	type maintenanceResult struct {
		token *InspectionMaintenance
		err   error
	}
	maintenance := make(chan maintenanceResult, 1)
	go func() { token, err := r.catalog.BeginMaintenance(ctx, 2); maintenance <- maintenanceResult{token, err} }()
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("maintenance did not reserve its generation")
	}
	if r.catalog.Version().Generation != 2 {
		t.Fatal("source generation not invalidated")
	}
	select {
	case early := <-maintenance:
		if early.token != nil {
			early.token.Close()
		}
		t.Fatal("physical maintenance began before audit I/O settled")
	default:
	}
	unblock()
	if err := <-results; !errors.Is(err, ErrInspectionChanged) {
		t.Fatalf("source generation not checked after audit: %v", err)
	}
	completed := <-maintenance
	if completed.err != nil {
		t.Fatal(completed.err)
	}
	completed.token.Close()
}

func TestRawInspectionDescriptorsDoNotMaterializeLargePayloadMetadata(t *testing.T) {
	r, _, _, record, path, _ := rawInspectionFixture(t)
	var metadata map[string]any
	if err := json.Unmarshal(record.PayloadMetadata, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["headers"] = append(metadata["headers"].([]any), map[string]any{"name": "X-Large-Retained-Header", "values": []string{strings.Repeat("x", 128<<10)}})
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE runtime_raw_evidence_envelopes SET payload_metadata=?,header_count=2 WHERE envelope_id=?`, encoded, record.EnvelopeID); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := r.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	// The list's descriptor fits this driver budget. Loading/sorting the retained
	// headers would exceed it, even though no header/body was requested.
	if _, err = sqlite.Limit(s.lease.conn, sqlite3.SQLITE_LIMIT_LENGTH, 64<<10); err != nil {
		t.Fatal(err)
	}
	rows, err := s.rows("exchange_id", record.ExchangeID, false)
	if err != nil || len(rows) != 1 || len(rows[0].record.PayloadMetadata) != 0 {
		t.Fatalf("descriptor query materialized retained payload metadata: %v", err)
	}
}
