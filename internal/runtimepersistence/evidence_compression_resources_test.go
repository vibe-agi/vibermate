package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestEvidenceCompressionColdWorkspaceIsBounded(t *testing.T) {
	const helperFlag = "VIBERMATE_TEST_EVIDENCE_COMPRESSION_COLD_WORKSPACE"
	if os.Getenv(helperFlag) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEvidenceCompressionColdWorkspaceIsBounded$", "-test.v")
		child.Env = append(os.Environ(), helperFlag+"=1")
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated evidence compression test: %v (context: %v)\n%s", err, ctx.Err(), output)
		}
		t.Logf("isolated evidence compression test:\n%s", output)
		return
	}

	// Restoring the machine-dependent workspace count must fail this fixture.
	// The boundary distinguishes one Best workspace from eight; it is not a
	// production request-memory budget. The child owns its GOMAXPROCS setting.
	runtime.GOMAXPROCS(8)
	input := []byte(`{"kind":"text","text":"` + strings.Repeat("x", 900) + `"}`)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	encoder := mustZstdWriter()
	output := make([][]byte, 4000)
	for i := range output {
		output[i] = encoder.EncodeAll(input, nil)
	}
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	runtime.KeepAlive(output)
	defer encoder.Close()
	t.Logf("cold evidence compression allocated %d bytes", allocated)
	if allocated > 96<<20 {
		t.Fatalf("bounded evidence compression allocated %d bytes", allocated)
	}
}

func TestEvidenceCompressionConcurrentPlaintextIdentity(t *testing.T) {
	// Shared-workspace corruption or a codec change that alters bytes must fail
	// for distinct callers, including empty, binary, small and large inputs.
	payloads := [][]byte{
		{},
		[]byte("synthetic evidence text"),
		{0x00, 0xff, 0x80, 0x01, 0x7f},
		bytes.Repeat([]byte("small repeated block\n"), 50),
		bytes.Repeat([]byte("large repeated block\n"), 20000),
		bodyForTest(0x31, 1<<20),
		bodyForTest(0xa4, 64<<10),
		[]byte(`{"kind":"text","text":"distinct last caller"}`),
	}
	encoder := mustZstdWriter()
	defer encoder.Close()
	encoded := make([][]byte, len(payloads))
	start := make(chan struct{})
	var callers sync.WaitGroup
	for i := range payloads {
		callers.Add(1)
		go func() {
			defer callers.Done()
			<-start
			encoded[i] = encoder.EncodeAll(payloads[i], nil)
		}()
	}
	close(start)
	callers.Wait()
	for i, want := range payloads {
		decoded, err := bodyDecoder.DecodeAll(encoded[i], make([]byte, 0, len(want)))
		if err != nil || !bytes.Equal(decoded, want) {
			t.Fatalf("caller %d: evidence compressor changed plaintext: %v", i, err)
		}
		if sha256.Sum256(decoded) != sha256.Sum256(want) {
			t.Fatalf("caller %d: plaintext digest changed", i)
		}
	}
}

func TestEvidenceCompressionRawCodecsSurviveReopen(t *testing.T) {
	// Both codec branches must survive the actual Raw repository and reopening;
	// using only highly compressible bodies would miss a broken identity fallback.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "raw.db")
	store := openTestStore(t, path)
	t.Cleanup(func() { shutdownTestStore(t, store) })
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	records := []rawevidence.StoredEnvelope{
		rawEvidenceRecordForTest("compression-raw.1", 1, rawevidence.LayerClientIngress,
			bytes.Repeat([]byte("synthetic raw evidence\n"), 4000), []byte(`{"version":1,"headers":[]}`)),
		rawEvidenceRecordForTest("compression-raw.2", 2, rawevidence.LayerClientDownstream,
			[]byte{0x00, 0x61, 0xfc, 0x89, 0x2e, 0x70, 0xe3, 0x15, 0x94, 0x3f, 0xa2, 0x58}, []byte(`{"version":1,"headers":[]}`)),
	}
	for i := range records {
		records[i].ObservedAt = at
		records[i].ExpiresAt = at.Add(time.Hour)
	}
	if err := store.RawEvidenceRepository().AppendBatch(ctx, records, at); err != nil {
		t.Fatal(err)
	}
	assertEvidenceCompressionCodecs(t, store, "runtime_evidence_chunks")
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	for _, want := range records {
		got, err := store.RawEvidenceRepository().GetEnvelope(ctx, want.EnvelopeID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Raw envelope %s changed after reopen: %v", want.EnvelopeID, err)
		}
	}
	if err := store.MaintainExpired(ctx, records[0].ExpiresAt); err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if _, err := store.RawEvidenceRepository().GetEnvelope(ctx, record.EnvelopeID); !errors.Is(err, rawevidence.ErrEnvelopeNotFound) {
			t.Fatalf("expired Raw envelope error = %v", err)
		}
	}
}

func TestEvidenceCompressionContentCodecsSurviveReopen(t *testing.T) {
	// Check the separate content-block writer and complete record readback, not
	// just the Raw chunk writer that happens to share its compressor.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "content.db")
	store := openTestStore(t, path)
	t.Cleanup(func() { shutdownTestStore(t, store) })
	at := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	want := contentRecordFixture(t, "compression-content", at)
	text := strings.Repeat("synthetic retained content\n", 4000)
	want.Request.Messages[0].Blocks[0].Text = text
	want.Request.Messages[0].Blocks[0].OriginalSize = len(text)
	want.Response.Blocks[0].Text = "Q"
	want.Response.Blocks[0].OriginalSize = 1
	if err := store.ExchangeContentRepository().Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	assertEvidenceCompressionCodecs(t, store, "runtime_exchange_content_blocks")
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	got, err := store.ExchangeContentRepository().Get(ctx, want.ExchangeID, at.Add(time.Minute))
	// Presentation is a derived read view, not part of the retained record.
	want.Presentation = exchangecontent.RequestPresentation{Mode: exchangecontent.RequestPresentationCheckpoint}
	var gotCanonical, wantCanonical bytes.Buffer
	if err == nil {
		err = exchangecontent.WriteCanonicalJSON(&gotCanonical, got)
	}
	if err == nil {
		err = exchangecontent.WriteCanonicalJSON(&wantCanonical, want)
	}
	if err != nil || !bytes.Equal(gotCanonical.Bytes(), wantCanonical.Bytes()) {
		t.Fatalf("complete content record changed after reopen: %v", err)
	}
	if !reflect.DeepEqual(got.Presentation, want.Presentation) {
		t.Fatalf("content presentation changed after reopen: got=%+v want=%+v", got.Presentation, want.Presentation)
	}
	if _, err := store.ExchangeContentRepository().Get(ctx, want.ExchangeID, want.ExpiresAt); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("expired content record error = %v", err)
	}
	if purged, err := store.ExchangeContentRepository().PurgeExpired(ctx, want.ExpiresAt); err != nil || purged != 1 {
		t.Fatalf("expired content purge = %d, %v", purged, err)
	}
}

func assertEvidenceCompressionCodecs(t *testing.T, store *Store, table string) {
	t.Helper()
	// Only these test-owned stores are inspected. Codec names are unchanged
	// format identities; both real persistence branches must have been exercised.
	for _, codec := range []string{"zstd", "identity"} {
		var count int
		if err := store.database.QueryRow("SELECT count(*) FROM "+table+" WHERE codec = ?", codec).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("%s fixture did not store codec %s", table, codec)
		}
	}
}
