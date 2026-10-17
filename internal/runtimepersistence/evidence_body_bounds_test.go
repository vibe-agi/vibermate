package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/evidencechunk"
)

// Use 128 occurrences for RED: the malformed graph occupies only 68 KiB but
// would expand to 8 MiB. This exposes the same missing bound as 8192 repeats
// without deliberately making the pre-fix test materialize 512 MiB.
func TestNativeCorruptBodyGraphRefusesBeforeLargeAllocation(t *testing.T) {
	for _, corruption := range []string{"repeated_manifest", "oversized_identity_payload", "oversized_zstd_payload"} {
		for _, page := range []bool{false, true} {
			name := corruption + "/full"
			if page {
				name = corruption + "/one_byte"
			}
			t.Run(name, func(t *testing.T) {
				store, value := nativeRetiredContentFixture(t)
				ctx := context.Background()
				if err := store.PutNativeContent(ctx, value); err != nil {
					t.Fatal(err)
				}
				bodyDigest := sha256.Sum256(value.Body)
				switch corruption {
				case "repeated_manifest":
					chunk := bytes.Repeat([]byte{'x'}, evidencechunk.MaximumBytes)
					digest := sha256.Sum256(chunk)
					if _, err := store.store.database.Exec(`INSERT INTO runtime_evidence_chunks(digest,plain_bytes,codec,payload) VALUES(?,?,'identity',?)`, digest[:], len(chunk), chunk); err != nil {
						t.Fatal(err)
					}
					manifest := bytes.Repeat(digest[:], 128)
					if _, err := store.store.database.Exec(`UPDATE runtime_evidence_bodies SET plain_bytes=?,chunk_manifest=? WHERE digest=?`, len(chunk), manifest, bodyDigest[:]); err != nil {
						t.Fatal(err)
					}
				default:
					codec := chunkCodecIdentity
					if corruption == "oversized_zstd_payload" {
						codec = chunkCodecZstd
					}
					if _, err := store.store.database.Exec(`UPDATE runtime_evidence_chunks SET codec=?,payload=zeroblob(?)`, codec, 8<<20); err != nil {
						t.Fatal(err)
					}
				}
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				var err error
				if page {
					_, err = store.ReadNativeContent(ctx, value.Identity.Scope.Capture, value.Identity.OperationID, string(value.Part), 0, 1)
				} else {
					_, err = store.GetNativeContent(ctx, value.Identity.OperationID, value.Part)
				}
				runtime.ReadMemStats(&after)
				if err == nil {
					t.Fatal("malformed graph returned content")
				}
				// The bounded manifest/metadata/refusal path needs far less than this.
				// Scanning the 8 MiB payload or reconstructing the corrupt body exceeds it.
				allocated := after.TotalAlloc - before.TotalAlloc
				t.Logf("refused %s with %d allocated bytes: %v", name, allocated, err)
				if allocated > 4<<20 {
					t.Fatalf("malformed graph allocated %d bytes before refusal (limit %d)", allocated, 4<<20)
				}
			})
		}
	}
}

// Exercise the review's exact 8192-occurrence arithmetic at the metadata seam.
// Even if the preflight regresses, this test cannot reconstruct a 512 MiB body.
func TestEvidenceChunkBoundsRejectMaximumRepeatedManifest(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "bounds.db"))
	defer shutdownTestStore(t, store)
	chunk := bytes.Repeat([]byte{'x'}, evidencechunk.MaximumBytes)
	digest := sha256.Sum256(chunk)
	if _, err := store.database.Exec(`INSERT INTO runtime_evidence_chunks(digest,plain_bytes,codec,payload) VALUES(?,?,'identity',?)`, digest[:], len(chunk), chunk); err != nil {
		t.Fatal(err)
	}
	manifest := bytes.Repeat(digest[:], maximumChunkManifestBytes/sha256.Size)
	_, err := loadEvidenceChunkBounds(context.Background(), store.database, manifest, len(chunk))
	if err == nil {
		t.Fatal("8192 occurrences passed a single-chunk declared body bound")
	}
}

func TestEvidenceBodyAcceptsValidRepeatedManifest(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "repeated.db"))
	defer shutdownTestStore(t, store)
	chunk := []byte("safe repeated chunk")
	body := bytes.Repeat(chunk, 3)
	digest := sha256.Sum256(chunk)
	bodyDigest := sha256.Sum256(body)
	if _, err := store.database.Exec(`INSERT INTO runtime_evidence_chunks(digest,plain_bytes,codec,payload) VALUES(?,?,'identity',?)`, digest[:], len(chunk), chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`INSERT INTO runtime_evidence_bodies(digest,plain_bytes,chunk_manifest) VALUES(?,?,?)`, bodyDigest[:], len(body), bytes.Repeat(digest[:], 3)); err != nil {
		t.Fatal(err)
	}
	got, err := loadEvidenceBody(context.Background(), store.database, bodyDigest[:])
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("valid repeated body rejected: %q %v", got, err)
	}
}

type changingEvidenceQueryer struct {
	*sql.DB
	queries int
	change  func() error
}

func (q *changingEvidenceQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	q.queries++
	if q.queries == 2 {
		if err := q.change(); err != nil {
			return nil, err
		}
	}
	return q.DB.QueryContext(ctx, query, args...)
}

func TestEvidencePayloadProjectionStaysBoundedAfterPreflightMutation(t *testing.T) {
	store, value := nativeRetiredContentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.PutNativeContent(ctx, value); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(value.Body)
	var before, after runtime.MemStats
	changed := false
	queryer := &changingEvidenceQueryer{DB: store.store.database, change: func() error {
		// Raw callers use a DB rather than a read transaction. Model a writer
		// changing the payload after metadata rows close, before payload selection.
		if _, err := store.store.database.ExecContext(ctx, `UPDATE runtime_evidence_chunks SET payload=zeroblob(?)`, 8<<20); err != nil {
			return err
		}
		changed = true
		runtime.GC()
		runtime.ReadMemStats(&before)
		return nil
	}}
	_, err := loadEvidenceBody(ctx, queryer, digest[:])
	runtime.ReadMemStats(&after)
	if !changed || queryer.queries != 2 {
		t.Fatal("mutation did not occur between the two indexed passes")
	}
	if err == nil {
		t.Fatal("post-preflight mutation returned content")
	}
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("post-preflight payload mutation refused with %d allocated bytes: %v", allocated, err)
	if allocated > 4<<20 {
		t.Fatalf("payload projection allocated %d bytes after bounds changed", allocated)
	}
}

func TestEvidenceChunkDecodeCannotExceedItsValidatedLength(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "decode.db"))
	defer shutdownTestStore(t, store)
	expanded := bytes.Repeat([]byte{'x'}, evidencechunk.MaximumBytes)
	digest := sha256.Sum256(expanded)
	bodyDigest := sha256.Sum256([]byte{'x'})
	encoded := bodyEncoder.EncodeAll(expanded, nil)
	if _, err := store.database.Exec(`INSERT INTO runtime_evidence_chunks(digest,plain_bytes,codec,payload) VALUES(?,1,'zstd',?)`, digest[:], encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`INSERT INTO runtime_evidence_bodies(digest,plain_bytes,chunk_manifest) VALUES(?,1,?)`, bodyDigest[:], digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEvidenceBody(context.Background(), store.database, bodyDigest[:]); err == nil {
		t.Fatal("compressed chunk exceeded its preflighted output capacity")
	}
}
