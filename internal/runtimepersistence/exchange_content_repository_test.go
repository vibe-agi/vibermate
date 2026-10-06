package runtimepersistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func candidateContentLimits() exchangecontent.SourceLimits {
	return exchangecontent.SourceLimits{
		Semantic:       protocolcore.ResourceLimits{Request: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}, Response: protocolcore.ResourceCost{PayloadBytes: 32 << 20, StructureBytes: 32 << 20}},
		Scratch:        protocolcore.ResourceCost{PayloadBytes: 128 << 20, StructureBytes: 32 << 20},
		CanonicalBytes: 256 << 20, RetainedBytes: 64 << 20, StructureBytes: 32 << 20,
	}
}

func openCandidateContentStore(t *testing.T, path string, limits *exchangecontent.SourceLimits) *Store {
	t.Helper()
	s, err := Open(context.Background(), Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout, ContentLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { shutdownTestStore(t, s) })
	return s
}

func putCandidateSource(t *testing.T, store *Store, source *exchangecontent.Source) error {
	t.Helper()
	sink, ok := any(store.exchangeContents).(interface {
		PutSource(context.Context, *exchangecontent.Source) error
	})
	if !ok {
		t.Fatal("real Store repository has no Source transaction")
	}
	return sink.PutSource(context.Background(), source)
}

func TestContentSourcePolicyBeforeOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "uncreated", "runtime.db")
	limits := candidateContentLimits()
	limits.RetainedBytes = 0
	s, err := Open(context.Background(), Options{DatabasePath: path, BusyTimeout: DefaultBusyTimeout, CommitReconcileTimeout: DefaultCommitReconcileTimeout, ContentLimits: &limits})
	if s != nil {
		shutdownTestStore(t, s)
	}
	if !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("invalid policy opened Store: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid policy changed filesystem: %v", err)
	}
}

func TestContentSourceReadsUsePool(t *testing.T) {
	l := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &l)
	r := contentRecordFixture(t, "read-pool", time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC))
	if err := s.exchangeContents.Put(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	writer, err := s.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.exchangeContents.Get(ctx, r.ExchangeID, r.RecordedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exchangeContents.GetPagedProjection(ctx, r.ExchangeID, r.RecordedAt, exchangecontent.RequestViewFull); err != nil {
		t.Fatal(err)
	}
	if _, err := s.exchangeContents.AvailableBodies(ctx, []string{r.ExchangeID}, r.RecordedAt); err != nil {
		t.Fatalf("body directory used writer: %v", err)
	}
	if _, err := s.exchangeContents.RequestPreviews(ctx, []string{r.ExchangeID}, r.RecordedAt); err != nil {
		t.Fatalf("preview used writer: %v", err)
	}
}

func TestContentSourcePublishesLongAndFramed(t *testing.T) {
	limits := candidateContentLimits()
	s := openCandidateContentStore(t, filepath.Join(t.TempDir(), "runtime.db"), &limits)
	// Constructor policy must not follow later caller changes.
	limits.RetainedBytes = 1
	sourceLimits := candidateContentLimits()
	record := contentRecordFixture(t, "source-long", time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC))
	first := record.Request.Messages[0]
	first.Blocks = []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("x", 1024), OriginalSize: 1024}}
	record.Request.Messages = make([]exchangecontent.Message, 4111)
	for i := range record.Request.Messages {
		record.Request.Messages[i] = first
	}
	record.Request.Messages[4110] = exchangecontent.Message{Role: "user", Blocks: []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: "complete-tail", OriginalSize: 13}}}
	record.Response.Blocks = []exchangecontent.Block{{Kind: "text", Availability: exchangecontent.AvailabilityRecorded, Text: strings.Repeat("&", 6<<20), OriginalSize: 6 << 20}}
	source, err := exchangecontent.SourceFromRecordWithin(sourceLimits, record)
	if err != nil {
		t.Fatal(err)
	}
	if err := putCandidateSource(t, s, source); err != nil {
		t.Fatal(err)
	}
	var count, slots, refs int
	if err := s.database.QueryRow(`SELECT request_message_count FROM runtime_exchange_contents WHERE exchange_id=?`, record.ExchangeID).Scan(&count); err != nil || count != 4111 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := s.database.QueryRow(`SELECT length(m.block_manifest)/64,(SELECT count(*) FROM runtime_exchange_content_block_refs r WHERE r.message_digest=m.digest) FROM runtime_exchange_content_messages m JOIN runtime_exchange_contents c ON c.response_message_digest=m.digest WHERE c.exchange_id=?`, record.ExchangeID).Scan(&slots, &refs); err != nil || slots != 2 || refs != 2 {
		t.Fatalf("framed slots=%d refs=%d err=%v", slots, refs, err)
	}
	got, err := s.exchangeContents.Get(context.Background(), record.ExchangeID, record.RecordedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("complete Source read: %v", err)
	}
	if len(got.Request.Messages) != 4111 || got.Request.Messages[4110].Blocks[0].Text != "complete-tail" || got.Response == nil || got.Response.Blocks[0].Text != record.Response.Blocks[0].Text {
		t.Fatal("Source read lost history or framed response")
	}
	paged, err := s.exchangeContents.GetPagedProjection(context.Background(), record.ExchangeID, record.RecordedAt.Add(time.Minute), exchangecontent.RequestViewFull)
	if err != nil {
		t.Fatalf("Source directory page: %v", err)
	}
	if paged.Response == nil || len(paged.Response.Blocks) != 1 || paged.Response.Blocks[0].Deferred == nil {
		t.Fatal("framed response must defer")
	}
	seen, next := len(paged.Request.Messages), paged.Page.RequestOffset
	for cursor := paged.Page.RequestNextCursor; cursor != ""; {
		history, err := s.exchangeContents.GetContentPage(context.Background(), record.ExchangeID, record.RecordedAt.Add(time.Minute), cursor)
		if err != nil || history.Kind != "request" || history.Total != 4111 || history.Offset+len(history.Messages) != next {
			t.Fatalf("history page at %d: %v", next, err)
		}
		for i, message := range history.Messages {
			if !reflect.DeepEqual(message, record.Request.Messages[history.Offset+i]) {
				t.Fatalf("history page changed occurrence %d", history.Offset+i)
			}
		}
		seen += len(history.Messages)
		next = history.Offset
		cursor = history.NextCursor
	}
	if seen != 4111 || next != 0 {
		t.Fatalf("history cursor reconstruction=%d at%d", seen, next)
	}
	page, err := s.exchangeContents.GetContentPage(context.Background(), record.ExchangeID, record.RecordedAt.Add(time.Minute), paged.Response.Blocks[0].Deferred.Cursor)
	if err != nil {
		t.Fatalf("Source message page: %v", err)
	}
	if page.Total != 1 || len(page.Blocks) != 1 || page.Blocks[0].Deferred == nil {
		t.Fatalf("physical slots were exposed as logical blocks: %+v", page)
	}
	body, err := s.exchangeContents.GetContentPage(context.Background(), record.ExchangeID, record.RecordedAt.Add(time.Minute), page.Blocks[0].Deferred.Cursor)
	if err != nil || body.Total != 6<<20 || body.Offset != 0 || body.Text != strings.Repeat("&", exchangecontent.PageBodyBytes) {
		t.Fatalf("Source body first page: total=%d bytes=%d err=%v", body.Total, len(body.Text), err)
	}
}

func TestExchangeContentRepositoryReopensAndExpiresEvidence(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	record := contentRecordFixture(t, "exchange-content", recordedAt)
	if err := repository.Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := repository.Put(context.Background(), record); err == nil {
		t.Fatal("duplicate content evidence was accepted")
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	got, err := reopened.ExchangeContentRepository().Get(
		context.Background(), record.ExchangeID, recordedAt.Add(time.Hour),
	)
	if err != nil || got.ExchangeID != record.ExchangeID || got.Response == nil ||
		got.Response.Usage.Output.Tokens != 2 ||
		!slices.Equal(got.Request.ProtocolEvidence, record.Request.ProtocolEvidence) ||
		!slices.Equal(got.Response.ProtocolEvidence, record.Response.ProtocolEvidence) {
		t.Fatalf("Get() = %+v, %v", got, err)
	}
	if _, err := reopened.ExchangeContentRepository().Get(
		context.Background(), record.ExchangeID, record.ExpiresAt,
	); !errors.Is(err, exchangecontent.ErrNotFound) {
		t.Fatalf("expired Get() error = %v", err)
	}
	purged, err := reopened.ExchangeContentRepository().PurgeExpired(
		context.Background(), record.ExpiresAt,
	)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeExpired() = %d, %v", purged, err)
	}
}

func TestExchangeContentRepositoryCompletesARecordedRequestInPlace(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 10, 1, 2, 3, 0, time.UTC)
	completed := contentRecordFixture(t, "exchange-live", recordedAt)
	pending := completed.Clone()
	pending.Response = nil
	if err := repository.Put(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	got, err := repository.Get(
		context.Background(), pending.ExchangeID, recordedAt.Add(time.Minute),
	)
	if err != nil || got.Response != nil || got.RecordedAt != recordedAt {
		t.Fatalf("pending Get() = %+v, %v", got, err)
	}
	completed.RecordedAt = recordedAt.Add(10 * time.Second)
	completed.ExpiresAt = completed.RecordedAt.AddDate(0, 0, 7)
	if err := repository.Put(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	got, err = repository.Get(
		context.Background(), completed.ExchangeID, recordedAt.Add(time.Minute),
	)
	if err != nil || got.Response == nil || got.RecordedAt != recordedAt ||
		got.ExpiresAt != pending.ExpiresAt {
		t.Fatalf("completed Get() = %+v, %v", got, err)
	}
}

func TestEmptyTerminalPersistsWithoutInventingATranscriptMessage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	at := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	record := contentRecordFixture(t, "empty-terminal", at)
	record.Response.Blocks = nil
	record.Response.StopReason = string(protocolcore.StopReasonIncomplete)
	pending := record.Clone()
	pending.Response = nil
	if err := store.ExchangeContentRepository().Put(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if err := store.ExchangeContentRepository().Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := store.ExchangeContentRepository().Put(context.Background(), record); err == nil {
		t.Fatal("completed record was overwritten")
	}
	shutdownTestStore(t, store)
	store = openTestStore(t, path)
	defer shutdownTestStore(t, store)
	got, err := store.ExchangeContentRepository().Get(context.Background(), record.ExchangeID, at.Add(time.Minute))
	if err != nil || got.Response == nil || len(got.Response.Blocks) != 0 || got.Response.Usage != record.Response.Usage || got.Response.StopReason != string(protocolcore.StopReasonIncomplete) {
		t.Fatalf("empty terminal: %+v, %v", got.Response, err)
	}
	projection, err := store.ExchangeContentRepository().GetProjection(context.Background(), record.ExchangeID, at.Add(time.Minute), exchangecontent.RequestViewIncremental)
	if err != nil || projection.Response == nil || len(projection.Response.Blocks) != 0 {
		t.Fatalf("projection: %+v, %v", projection, err)
	}
	var requestCount, expectedCount int
	var noResponseMessage bool
	if err := store.database.QueryRow(`SELECT request_message_count,expected_message_count,response_message_digest IS NULL FROM runtime_exchange_contents WHERE exchange_id=?`, record.ExchangeID).Scan(&requestCount, &expectedCount, &noResponseMessage); err != nil || requestCount != expectedCount || !noResponseMessage {
		t.Fatalf("empty output invented a transcript message: %d/%d %v %v", requestCount, expectedCount, noResponseMessage, err)
	}
}

func TestExchangeContentRepositorySharesExactHistoryAndDerivesIncrementalViews(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 9, 2, 0, 0, 0, time.UTC)
	managedParent := exchangecontent.ParentRef{CaptureRunID: "run-transcript"}

	first := transcriptContentRecordFixture(
		t,
		"exchange-first",
		recordedAt,
		managedParent,
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "first question"}},
		"first answer",
	)
	second := transcriptContentRecordFixture(
		t,
		"exchange-second",
		recordedAt.Add(time.Minute),
		managedParent,
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "first question"},
			{role: protocolcore.RoleAssistant, text: "first answer"},
			{role: protocolcore.RoleUser, text: "second question"},
		},
		"second answer",
	)
	retry := transcriptContentRecordFixture(
		t,
		"exchange-retry",
		recordedAt.Add(2*time.Minute),
		managedParent,
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "first question"},
			{role: protocolcore.RoleAssistant, text: "first answer"},
		},
		"",
	)
	checkpoint := transcriptContentRecordFixture(
		t,
		"exchange-checkpoint",
		recordedAt.Add(3*time.Minute),
		managedParent,
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "compacted history"}},
		"",
	)
	otherRun := transcriptContentRecordFixture(
		t,
		"exchange-other-run",
		recordedAt.Add(4*time.Minute),
		exchangecontent.ParentRef{CaptureRunID: "run-other"},
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "first question"},
			{role: protocolcore.RoleAssistant, text: "first answer"},
			{role: protocolcore.RoleUser, text: "second question"},
		},
		"",
	)
	manual := transcriptContentRecordFixture(
		t,
		"exchange-manual",
		recordedAt.Add(5*time.Minute),
		exchangecontent.ParentRef{ManualCaptureID: "manual-shared-proxy"},
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "first question"},
			{role: protocolcore.RoleAssistant, text: "first answer"},
			{role: protocolcore.RoleUser, text: "second question"},
		},
		"",
	)
	for _, record := range []exchangecontent.Record{first, second, retry, checkpoint, otherRun, manual} {
		if err := repository.Put(context.Background(), record); err != nil {
			t.Fatalf("Put(%s): %v", record.ExchangeID, err)
		}
	}
	previews, err := repository.RequestPreviews(
		context.Background(),
		[]string{"exchange-first", "exchange-second", "exchange-missing"},
		recordedAt.Add(time.Hour),
	)
	if err != nil || len(previews) != 2 ||
		previews["exchange-first"].Text != "first question" ||
		previews["exchange-second"].Text != "second question" {
		t.Fatalf("RequestPreviews() = %+v, %v", previews, err)
	}
	availability, err := repository.AvailableBodies(context.Background(), []string{"exchange-first", "exchange-missing"}, recordedAt.Add(time.Hour))
	if err != nil || !availability["exchange-first"] || availability["exchange-missing"] {
		t.Fatalf("body availability: %v %v", availability, err)
	}
	expired, err := repository.AvailableBodies(context.Background(), []string{"exchange-first"}, first.ExpiresAt)
	if err != nil || expired["exchange-first"] {
		t.Fatalf("expired body availability: %v %v", expired, err)
	}

	assertPresentation := func(
		exchangeID string,
		mode exchangecontent.RequestPresentationMode,
		inherited int,
		wantCount int,
		wantLastText string,
	) {
		t.Helper()
		got, err := repository.Get(context.Background(), exchangeID, recordedAt.Add(time.Hour))
		if err != nil {
			t.Fatalf("Get(%s): %v", exchangeID, err)
		}
		if got.Presentation.Mode != mode || got.Presentation.InheritedMessageCount != inherited {
			t.Fatalf("Get(%s) presentation = %+v", exchangeID, got.Presentation)
		}
		incremental := got.IncrementalRequest()
		if len(incremental) != wantCount {
			t.Fatalf("Get(%s) incremental messages = %+v", exchangeID, incremental)
		}
		if wantLastText != "" && (len(incremental[wantCount-1].Blocks) != 1 ||
			incremental[wantCount-1].Blocks[0].Text != wantLastText) {
			t.Fatalf("Get(%s) incremental messages = %+v", exchangeID, incremental)
		}
	}
	assertPresentation("exchange-first", exchangecontent.RequestPresentationCheckpoint, 0, 1, "first question")
	assertPresentation("exchange-second", exchangecontent.RequestPresentationIncremental, 2, 1, "second question")
	assertPresentation("exchange-retry", exchangecontent.RequestPresentationSameTranscript, 2, 0, "")
	assertPresentation("exchange-checkpoint", exchangecontent.RequestPresentationCheckpoint, 0, 1, "compacted history")
	assertPresentation("exchange-other-run", exchangecontent.RequestPresentationCheckpoint, 0, 3, "second question")
	assertPresentation("exchange-manual", exchangecontent.RequestPresentationCheckpoint, 0, 3, "second question")

	assertProjection := func(
		exchangeID string,
		view exchangecontent.RequestView,
		wantCount int,
		wantTotal int,
		wantRelationship exchangecontent.RequestPresentationMode,
	) {
		t.Helper()
		projection, err := repository.GetProjection(
			context.Background(), exchangeID, recordedAt.Add(time.Hour), view,
		)
		if err != nil || projection.Validate() != nil ||
			projection.View != view || len(projection.Request.Messages) != wantCount ||
			projection.TotalMessageCount != wantTotal ||
			projection.Presentation.Mode != wantRelationship {
			t.Fatalf("GetProjection(%s, %s) = %+v, %v", exchangeID, view, projection, err)
		}
	}
	assertProjection(
		"exchange-second", exchangecontent.RequestViewIncremental, 1, 3,
		exchangecontent.RequestPresentationIncremental,
	)
	assertProjection(
		"exchange-second", exchangecontent.RequestViewFull, 3, 3,
		exchangecontent.RequestPresentationIncremental,
	)
	assertProjection(
		"exchange-retry", exchangecontent.RequestViewIncremental, 0, 2,
		exchangecontent.RequestPresentationSameTranscript,
	)
	assertProjection(
		"exchange-checkpoint", exchangecontent.RequestViewIncremental, 1, 1,
		exchangecontent.RequestPresentationCheckpoint,
	)

	var messageCount, transcriptCount int
	if err := store.database.QueryRow(
		`SELECT count(*) FROM runtime_exchange_content_messages`,
	).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	if err := store.database.QueryRow(
		`SELECT count(*) FROM runtime_exchange_content_transcripts`,
	).Scan(&transcriptCount); err != nil {
		t.Fatal(err)
	}
	// Six full request records contain thirteen message occurrences. The local
	// store retains only five distinct message payloads and five transcript
	// nodes; the upstream requests remain unchanged and complete.
	if messageCount != 5 || transcriptCount != 5 {
		t.Fatalf("content-addressed counts = messages %d, transcripts %d", messageCount, transcriptCount)
	}
}

func TestExchangeContentIncrementalProjectionReadsOnlyVerifiedSuffix(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 15, 2, 0, 0, 0, time.UTC)
	parent := exchangecontent.ParentRef{CaptureRunID: "run-bounded-projection"}
	first := transcriptContentRecordFixture(
		t,
		"exchange-prefix",
		recordedAt,
		parent,
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "old prefix"}},
		"old answer",
	)
	second := transcriptContentRecordFixture(
		t,
		"exchange-suffix",
		recordedAt.Add(time.Minute),
		parent,
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "old prefix"},
			{role: protocolcore.RoleAssistant, text: "old answer"},
			{role: protocolcore.RoleUser, text: "new suffix"},
		},
		"new answer",
	)
	for _, record := range []exchangecontent.Record{first, second} {
		if err := repository.Put(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}

	// Damage a payload that belongs only to the inherited prefix. The bounded
	// projection authenticates the stored base root and reads only the new
	// suffix; an explicit full read still detects the historical corruption.
	// Tampering now targets a content block, which is where a message's bytes
	// live. The message digest is still SHA-256 of its canonical JSON, so
	// rebuilding it from a rewritten block must fail that check.
	if _, err := store.database.Exec(
		`UPDATE runtime_exchange_content_blocks
		 SET payload = ?, plain_bytes = length(CAST(? AS BLOB)),
		     codec = 'identity'
		 WHERE digest = (
		   SELECT substr(block_manifest, 1, 64)
		     FROM runtime_exchange_content_messages
		    WHERE digest = (
		      SELECT message_digest FROM runtime_exchange_content_transcripts
		      WHERE depth = 1 LIMIT 1
		    )
		 )`,
		[]byte(`{"kind":"text","availability":"recorded","text":"tampered","originalSize":8}`),
		[]byte(`{"kind":"text","availability":"recorded","text":"tampered","originalSize":8}`),
	); err != nil {
		t.Fatal(err)
	}
	projection, err := repository.GetProjection(
		context.Background(), second.ExchangeID, recordedAt.Add(time.Hour),
		exchangecontent.RequestViewIncremental,
	)
	if err != nil || len(projection.Request.Messages) != 1 ||
		projection.Request.Messages[0].Blocks[0].Text != "new suffix" ||
		projection.TotalMessageCount != 3 {
		t.Fatalf("incremental projection = %+v, %v", projection, err)
	}
	if _, err := repository.Get(
		context.Background(), second.ExchangeID, recordedAt.Add(time.Hour),
	); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("full Get() error = %v", err)
	}
}

func TestExchangeContentRepositoryKeepsLiveDescendantsAfterParentExpiry(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.ExchangeContentRepository()
	recordedAt := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	parent := exchangecontent.ParentRef{CaptureRunID: "run-retention"}
	first := transcriptContentRecordFixture(
		t,
		"exchange-expiring-parent",
		recordedAt,
		parent,
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "first question"}},
		"first answer",
	)
	second := transcriptContentRecordFixture(
		t,
		"exchange-live-child",
		recordedAt.Add(24*time.Hour),
		parent,
		[]transcriptMessage{
			{role: protocolcore.RoleUser, text: "first question"},
			{role: protocolcore.RoleAssistant, text: "first answer"},
			{role: protocolcore.RoleUser, text: "second question"},
		},
		"second answer",
	)
	if err := repository.Put(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := repository.Put(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	purged, err := repository.PurgeExpired(context.Background(), first.ExpiresAt)
	if err != nil || purged != 1 {
		t.Fatalf("PurgeExpired() = %d, %v", purged, err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	defer func() {
		if err := reopened.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	got, err := reopened.ExchangeContentRepository().Get(
		context.Background(), second.ExchangeID, first.ExpiresAt.Add(time.Hour),
	)
	if err != nil || len(got.Request.Messages) != 3 || got.Response == nil ||
		got.Response.Blocks[0].Text != "second answer" ||
		got.Presentation.Mode != exchangecontent.RequestPresentationIncremental ||
		got.Presentation.InheritedMessageCount != 2 {
		t.Fatalf("reopened descendant = %+v, %v", got, err)
	}
}

func TestExchangeContentRepositoryRejectsTamperedSharedMessagePayload(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	recordedAt := time.Date(2026, 8, 9, 4, 0, 0, 0, time.UTC)
	record := transcriptContentRecordFixture(
		t,
		"exchange-tampered-message",
		recordedAt,
		exchangecontent.ParentRef{CaptureRunID: "run-tamper"},
		[]transcriptMessage{{role: protocolcore.RoleUser, text: "original"}},
		"answer",
	)
	if err := store.ExchangeContentRepository().Put(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(
		`UPDATE runtime_exchange_content_blocks
		 SET payload = ?, plain_bytes = length(CAST(? AS BLOB)),
		     codec = 'identity'
		 WHERE digest = (
		   SELECT substr(block_manifest, 1, 64)
		     FROM runtime_exchange_content_messages
		    WHERE digest = (
		      SELECT message_digest FROM runtime_exchange_content_transcripts
		      WHERE digest = (
		        SELECT request_transcript_digest FROM runtime_exchange_contents
		        WHERE exchange_id = ?
		      )
		    )
		 )`,
		[]byte(`{"kind":"text","availability":"recorded","text":"tampered","originalSize":8}`),
		[]byte(`{"kind":"text","availability":"recorded","text":"tampered","originalSize":8}`),
		record.ExchangeID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ExchangeContentRepository().Get(
		context.Background(), record.ExchangeID, recordedAt.Add(time.Hour),
	); !errors.Is(err, exchangecontent.ErrInvalidEvidence) {
		t.Fatalf("tampered Get() error = %v", err)
	}
}

type transcriptMessage struct {
	role protocolcore.Role
	text string
}

func transcriptContentRecordFixture(
	t *testing.T,
	exchangeID string,
	recordedAt time.Time,
	parent exchangecontent.ParentRef,
	messages []transcriptMessage,
	responseText string,
) exchangecontent.Record {
	t.Helper()
	requestMessages := make([]protocolcore.Message, 0, len(messages))
	for _, message := range messages {
		block, err := protocolcore.NewTextBlock(message.text)
		if err != nil {
			t.Fatal(err)
		}
		requestMessages = append(requestMessages, protocolcore.Message{
			Role: message.role, Blocks: []protocolcore.ContentBlock{block},
		})
	}
	request := protocolcore.Request{
		RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 16,
		Messages: requestMessages,
	}
	var response *protocolcore.Response
	if responseText != "" {
		block, err := protocolcore.NewTextBlock(responseText)
		if err != nil {
			t.Fatal(err)
		}
		response = &protocolcore.Response{
			ID:             "response-" + exchangeID,
			RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model",
			Blocks: []protocolcore.ContentBlock{block}, StopReason: protocolcore.StopReasonEndTurn,
		}
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
		request,
		response,
		exchangecontent.WithParentRef(parent),
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func contentRecordFixture(t testing.TB, exchangeID string, recordedAt time.Time) exchangecontent.Record {
	t.Helper()
	block, err := protocolcore.NewTextBlock("hello")
	if err != nil {
		t.Fatal(err)
	}
	request := protocolcore.Request{
		RequestedModel: "model", EffectiveModel: "model", MaxOutputTokens: 16,
		Messages: []protocolcore.Message{{Role: protocolcore.RoleUser, Blocks: []protocolcore.ContentBlock{block}}},
		ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{{
			Name: "client.session_id", Value: "session-1",
		}},
	}
	response := protocolcore.Response{
		ID: "response", RequestedModel: "model", EffectiveModel: "model", ReportedModel: "model",
		Blocks: []protocolcore.ContentBlock{block}, StopReason: protocolcore.StopReasonEndTurn,
		Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Known: true, Tokens: 2, Source: "provider"}},
		ProtocolEvidence: []protocolcore.ProtocolEvidenceValue{{
			Name: "provider.output.0000.id", Value: "message-1",
		}},
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
		environment.DefaultContentRecordingPolicy(), recordedAt, request, &response,
	)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
