package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

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

func contentRecordFixture(t *testing.T, exchangeID string, recordedAt time.Time) exchangecontent.Record {
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
