package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

// Each physical row must authenticate before it can be interpreted as JSON or
// a fragment. A well-formed, same-length replacement is still corruption.
func TestStoredBlockRejectsPhysicalDigestMismatch(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "physical-digest.db"))
	defer shutdownTestStore(t, store)
	ctx := context.Background()
	block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "first", OriginalSize: 5}
	digest, encoded, err := encodeStoredBlock(block)
	if err != nil {
		t.Fatal(err)
	}
	damaged := strings.Replace(string(encoded), "first", "other", 1)
	if _, err := store.database.ExecContext(ctx, `INSERT INTO runtime_exchange_content_blocks(digest,plain_bytes,codec,payload) VALUES(?,?,'identity',?)`, digest, len(damaged), []byte(damaged)); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStoredBlock(ctx, store.database, digest); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("same-length substituted physical row accepted: %v", err)
	}
	if _, err := loadStoredBlocksByDigest(ctx, store.database, []string{digest}); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("batch accepted same-length substituted physical row: %v", err)
	}
}

func TestStoredPhysicalPayloadCodecAndDeclaredLength(t *testing.T) {
	block := exchangecontent.Block{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("first", 1024), OriginalSize: 5120}
	digest, encoded, err := encodeStoredBlock(block)
	if err != nil {
		t.Fatal(err)
	}
	compressed := bodyEncoder.EncodeAll(encoded, nil)
	for _, tc := range []struct {
		name, codec, digest string
		plain               int
		payload             []byte
		valid               bool
	}{
		{"identity", "identity", digest, len(encoded), encoded, true},
		{"zstd", "zstd", digest, len(encoded), compressed, true},
		{"undersized declaration", "zstd", digest, len(encoded) - 1, compressed, false},
		{"oversized declaration", "zstd", digest, len(encoded) + 1, compressed, false},
		{"identity length mismatch", "identity", digest, len(encoded) - 1, encoded, false},
		{"unknown codec", "unknown", digest, len(encoded), encoded, false},
		{"physical limit", "identity", digest, exchangecontent.MaxEncodedBytes + 1, encoded, false},
		{"digest mismatch", "zstd", strings.Repeat("0", 64), len(encoded), compressed, false},
		{"invalid digest", "identity", "bad", len(encoded), encoded, false},
		{"empty payload", "identity", digest, len(encoded), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeStoredPhysicalPayload(tc.digest, tc.plain, tc.codec, tc.payload)
			if tc.valid {
				if err != nil || string(got) != string(encoded) {
					t.Fatalf("valid row changed: %v", err)
				}
				return
			}
			if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
				t.Fatalf("damaged physical row accepted: %v", err)
			}
		})
	}
}

func blockRecordFixture(
	t testing.TB,
	exchangeID string,
	recordedAt time.Time,
	systemTexts []string,
	userText string,
) exchangecontent.Record {
	t.Helper()
	systemBlocks := make([]protocolcore.ContentBlock, 0, len(systemTexts))
	for _, text := range systemTexts {
		block, err := protocolcore.NewTextBlock(text)
		if err != nil {
			t.Fatal(err)
		}
		systemBlocks = append(systemBlocks, block)
	}
	userBlock, err := protocolcore.NewTextBlock(userText)
	if err != nil {
		t.Fatal(err)
	}
	record, err := exchangecontent.NewRecord(
		exchangeID,
		exchangecontent.FrozenRef{
			EnvironmentID: "work", EnvironmentRevision: 1,
			EnvironmentDigest: strings.Repeat("a", 64),
			ClientEndpointID:  "endpoint", ClientEndpointRevision: 1,
			ProtocolPlanID: "plan", ProtocolPlanRevision: 1,
			RouteID: "route", RouteRevision: 1,
		},
		environment.DefaultContentRecordingPolicy(),
		recordedAt,
		protocolcore.Request{
			RequestedModel: "model", EffectiveModel: "model",
			MaxOutputTokens: 16,
			System:          systemBlocks,
			Messages: []protocolcore.Message{{
				Role:   protocolcore.RoleUser,
				Blocks: []protocolcore.ContentBlock{userBlock},
			}},
		},
		nil,
		exchangecontent.WithParentRef(
			exchangecontent.ParentRef{CaptureRunID: "run-blocks"},
		),
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// The protocol's structure is Exchange → ordered messages → ordered blocks. The
// store used to address a message by the digest of its whole payload, so a
// message differing in one block was stored again in full. Measured on a real
// corpus that cost 40.1% of all stored blocks. Blocks are the protocol's atom
// and, as Phase 5 measured, also the storage optimum.
func TestOnlyTheChangedBlockIsStoredAgain(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 17, 5, 0, 0, 0, time.UTC)

	const shared = "You are an interactive agent. " +
		"Follow the instructions below and be precise about evidence."
	first := blockRecordFixture(
		t, "exchange-blocks-first", recordedAt,
		[]string{"x-anthropic-billing-header: cch=aaaaa;", shared},
		"first question",
	)
	if err := repository.Put(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	blocksAfterFirst := countRows(t, store, "runtime_exchange_content_blocks")
	if blocksAfterFirst == 0 {
		t.Fatal("the first record stored no blocks")
	}

	// Only the telemetry block changed. The long instruction block is identical.
	second := blockRecordFixture(
		t, "exchange-blocks-second", recordedAt.Add(time.Minute),
		[]string{"x-anthropic-billing-header: cch=bbbbb;", shared},
		"first question",
	)
	if err := repository.Put(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	added := countRows(t, store, "runtime_exchange_content_blocks") - blocksAfterFirst
	if added != 1 {
		t.Fatalf("storing a one-block change added %d blocks, want 1", added)
	}
}

// Retention has to reach the blocks. A message row released by expiry leaves its
// blocks unreachable, and an unreachable block that stays on disk means retention
// cost stops tracking retained content — which is the whole point of the sixth
// required item.
func TestExpiredExchangesReleaseTheirContentBlocks(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)

	record := blockRecordFixture(
		t, "exchange-blocks-expiring", recordedAt,
		[]string{"instruction that should not outlive its Exchange"},
		"question that should not outlive its Exchange",
	)
	if err := repository.Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if countRows(t, store, "runtime_exchange_content_blocks") == 0 {
		t.Fatal("the record stored no blocks")
	}

	// Well past the default retention window.
	if _, err := repository.PurgeExpired(
		context.Background(), recordedAt.AddDate(0, 0, 400),
	); err != nil {
		t.Fatal(err)
	}
	if got := countRows(t, store, "runtime_exchange_content_messages"); got != 0 {
		t.Fatalf("messages = %d after expiry, want 0", got)
	}
	if got := countRows(t, store, "runtime_exchange_content_blocks"); got != 0 {
		t.Fatalf(
			"blocks = %d after expiry; unreachable content still occupies the database",
			got,
		)
	}
}

// Phase 5 changes where a message's bytes live, never what its identity is.
func TestBlockStoragePreservesMessageIdentityAndContent(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 17, 6, 0, 0, 0, time.UTC)

	record := blockRecordFixture(
		t, "exchange-blocks-identity", recordedAt,
		[]string{"first instruction", "second instruction"},
		"the question",
	)
	if err := repository.Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	loaded, err := repository.Get(
		context.Background(),
		"exchange-blocks-identity",
		recordedAt.Add(time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Request.System) != 2 ||
		loaded.Request.System[0].Text != "first instruction" ||
		loaded.Request.System[1].Text != "second instruction" {
		t.Fatalf("system blocks changed: %+v", loaded.Request.System)
	}
	if len(loaded.Request.Messages) != 1 ||
		len(loaded.Request.Messages[0].Blocks) != 1 ||
		loaded.Request.Messages[0].Blocks[0].Text != "the question" {
		t.Fatalf("message blocks changed: %+v", loaded.Request.Messages)
	}
}
