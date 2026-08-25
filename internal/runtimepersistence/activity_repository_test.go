package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

func TestActivityRepositoryGetsExactlyOneExchangeTerminal(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	appendRecord := func(id string, kind activity.Kind, subject string) activity.Record {
		t.Helper()
		candidate := activity.Record{
			ID:         id,
			OccurredAt: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC),
			Kind:       kind,
			SubjectID:  subject,
			Status:     activity.StatusFailed,
			ReasonCode: "provider_transport_failed",
		}
		if kind == activity.KindExchangeCompleted {
			setFrozenExecutionEvidence(&candidate, "detail")
			candidate.SourceKind = activity.SourceSystemProxy
			candidate.SourceDisplayName = "ViberMate runtime"
			candidate.SourceRecognition = activity.SourceRecognitionUnknown
			candidate.ConnectionID = "connection-" + subject
		}
		record, err := repository.Append(context.Background(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	appendRecord("activity-control", activity.KindApprovalResolved, "exchange-detail")
	want := appendRecord(
		"activity-exchange-detail",
		activity.KindExchangeCompleted,
		"exchange-detail",
	)
	got, err := repository.GetExchange(context.Background(), "exchange-detail")
	if err != nil || got.ID != want.ID || got.SubjectID != "exchange-detail" {
		t.Fatalf("GetExchange() = %+v, %v", got, err)
	}
	if _, err := repository.GetExchange(
		context.Background(),
		"exchange-missing",
	); !errors.Is(err, activity.ErrExchangeNotFound) {
		t.Fatalf("missing GetExchange() error = %v", err)
	}
	appendRecord(
		"activity-exchange-detail-duplicate",
		activity.KindExchangeCompleted,
		"exchange-detail",
	)
	if _, err := repository.GetExchange(
		context.Background(),
		"exchange-detail",
	); err == nil || errors.Is(err, activity.ErrExchangeNotFound) {
		t.Fatalf("duplicate GetExchange() error = %v", err)
	}
}

func TestActivityRepositoryPersistsOriginalDestinationWithoutSyntheticRoute(
	t *testing.T,
) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	record := activity.Record{
		ID:                "activity-original-destination",
		OccurredAt:        time.Date(2026, 8, 24, 17, 54, 0, 0, time.UTC),
		Kind:              activity.KindExchangeCompleted,
		SubjectID:         "exchange-original-destination",
		Status:            activity.StatusSucceeded,
		SourceKind:        activity.SourceCaptureRun,
		SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionConfigured,
		CaptureRunID:      "run-original-destination",
		ConnectionID:      "connection-original-destination",
	}
	setFrozenExecutionEvidence(&record, "original-destination")
	record.RouteID = ""
	record.RouteRevision = 0
	record.AccountID = ""
	record.AccountRevision = 0
	record.CredentialEpoch = 0

	stored, err := repository.Append(context.Background(), record)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repository.GetExchange(
		context.Background(),
		"exchange-original-destination",
	)
	if err != nil || got.ID != stored.ID || got.RouteID != "" ||
		got.RouteRevision != 0 || got.AccountID != "" {
		t.Fatalf("Original Destination Activity = %+v, %v", got, err)
	}
}

func TestConversationIdentitySurvivesSQLiteReopenWithoutLosingNativeIDs(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	identity := agentconversation.ClientIdentity{
		Client: "claude", SessionID: "session-resumable", SessionResumable: true,
		ActorID: "agent-review", ActorLabel: "code-review",
		ActorType: "general-purpose", ActorIsSubagent: true,
		ProviderResponseID: "msg-provider", ProviderMessageID: "msg-provider",
		Source: "client_local_state", Confidence: "exact",
		ObservedAt: time.Date(2026, 8, 14, 2, 3, 4, 567000000, time.UTC),
		ProtocolIDs: []agentconversation.ClientEvidenceValue{
			{Name: "claude.agent_id", Value: "agent-review"},
			{Name: "claude.parent_agent_id", Value: "agent-main"},
			{Name: "claude.session_id", Value: "session-resumable"},
			{Name: "claude.tool_use_id", Value: "tool-agent-spawn"},
		},
		Attributes: []agentconversation.ClientEvidenceValue{
			{Name: "claude.description", Value: "code-review"},
			{Name: "claude.spawn_depth", Value: "1"},
		},
	}
	if err := store.ConversationIdentityRepository().PutConversationIdentity(
		context.Background(), "exchange-native-identity", identity,
	); err != nil {
		t.Fatal(err)
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
	stored, err := reopened.ConversationIdentityRepository().GetConversationIdentity(
		context.Background(), "exchange-native-identity",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(identity) {
		t.Fatalf("reopened identity = %#v, want %#v", stored, identity)
	}
}

func TestConversationIdentityPersistsWireEvidenceThenDeepensFromLocalState(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.ConversationIdentityRepository()
	observedAt := time.Date(2026, 8, 14, 11, 31, 5, 0, time.UTC)
	wire, found := agentconversation.ClientIdentityFromProtocolEvidence(
		[]protocolcore.ProtocolEvidenceValue{
			{Name: "claude.agent_id", Value: "agent-review"},
			{Name: "claude.parent_agent_id", Value: "agent-main"},
			{Name: "claude.session_id", Value: "session-resumable"},
		},
		"",
		observedAt,
	)
	if !found {
		t.Fatal("wire identity was not derived")
	}
	if err := repository.PutConversationIdentity(
		context.Background(), "exchange-wire", wire,
	); err != nil {
		t.Fatal(err)
	}
	local := wire.Clone()
	local.ActorLabel = "Angle A line-by-line scan"
	local.ActorType = "general-purpose"
	local.ProviderResponseID = "msg-provider"
	local.ProviderMessageID = "msg-provider"
	local.Source = agentconversation.ClientIdentitySourceLocalState
	local.ObservedAt = observedAt.Add(time.Second)
	local.ProtocolIDs = append(local.ProtocolIDs,
		agentconversation.ClientEvidenceValue{
			Name: "claude.request_id", Value: "request-1",
		},
	)
	slices.SortFunc(local.ProtocolIDs, func(left, right agentconversation.ClientEvidenceValue) int {
		if byName := strings.Compare(left.Name, right.Name); byName != 0 {
			return byName
		}
		return strings.Compare(left.Value, right.Value)
	})
	local.Attributes = []agentconversation.ClientEvidenceValue{
		{Name: "claude.description", Value: "Angle A line-by-line scan"},
	}
	if err := repository.PutConversationIdentity(
		context.Background(), "exchange-wire", local,
	); err != nil {
		t.Fatal(err)
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
	stored, err := reopened.ConversationIdentityRepository().GetConversationIdentity(
		context.Background(), "exchange-wire",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Source != agentconversation.ClientIdentitySourceLocalState ||
		stored.ActorLabel != local.ActorLabel ||
		stored.ProviderResponseID != "msg-provider" ||
		!stored.ObservedAt.Equal(observedAt) ||
		len(stored.ProtocolIDs) != 4 {
		t.Fatalf("deepened identity = %#v", stored)
	}
}

func TestActivityRepositoryMaterializesPendingThenTerminalExchange(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	appendExchange := func(id string, kind activity.Kind, status activity.Status) activity.Record {
		t.Helper()
		candidate := activity.Record{
			ID:                id,
			OccurredAt:        time.Date(2026, 8, 10, 2, 0, 0, 0, time.UTC),
			Kind:              kind,
			SubjectID:         "exchange-live",
			Status:            status,
			SourceKind:        activity.SourceCaptureRun,
			SourceDisplayName: "claude",
			SourceRecognition: activity.SourceRecognitionConfigured,
			CaptureRunID:      "run-live",
			ConnectionID:      "connection-live",
		}
		setFrozenExecutionEvidence(&candidate, "live")
		if kind == activity.KindExchangeStarted {
			candidate.AccountID = ""
			candidate.AccountRevision = 0
			candidate.CredentialEpoch = 0
		}
		record, err := repository.Append(context.Background(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		return record
	}
	started := appendExchange(
		"activity-exchange-started", activity.KindExchangeStarted, activity.StatusPending,
	)
	conversations, err := repository.ListConversations(
		context.Background(), activity.ConversationIndexRequest{Limit: 10},
	)
	if err != nil || len(conversations.Items) != 1 ||
		conversations.Items[0].Conversation.Kind != agentconversation.KindPendingExchange ||
		conversations.Items[0].Latest.ID != started.ID {
		t.Fatalf("pending Conversation index = %+v, %v", conversations, err)
	}
	page, err := repository.ListExchanges(
		context.Background(), activity.PageRequest{Limit: 10, CaptureRunID: "run-live"},
	)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != started.ID ||
		page.Items[0].Status != activity.StatusPending {
		t.Fatalf("pending page = %+v, %v", page, err)
	}
	terminal := appendExchange(
		"activity-exchange-completed", activity.KindExchangeCompleted, activity.StatusSucceeded,
	)
	conversations, err = repository.ListConversations(
		context.Background(), activity.ConversationIndexRequest{Limit: 10},
	)
	if err != nil || len(conversations.Items) != 1 ||
		conversations.Items[0].Conversation.Kind != agentconversation.KindIsolatedExchange ||
		conversations.Items[0].Latest.ID != terminal.ID {
		t.Fatalf("terminal Conversation index = %+v, %v", conversations, err)
	}
	page, err = repository.ListExchanges(
		context.Background(), activity.PageRequest{Limit: 10, CaptureRunID: "run-live"},
	)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != terminal.ID ||
		page.Items[0].Status != activity.StatusSucceeded {
		t.Fatalf("terminal page = %+v, %v", page, err)
	}
	got, err := repository.GetExchange(context.Background(), "exchange-live")
	if err != nil || got.ID != terminal.ID {
		t.Fatalf("GetExchange() = %+v, %v", got, err)
	}
}

func TestActivityRepositoryListsExchangePagesWithoutSkips(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.ActivityRepository()
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	appendRecord := func(id string, kind activity.Kind, subject string) activity.Record {
		t.Helper()
		candidate := activity.Record{
			ID:         id,
			OccurredAt: now,
			Kind:       kind,
			SubjectID:  subject,
			Status:     activity.StatusSucceeded,
		}
		if kind == activity.KindExchangeCompleted {
			setFrozenExecutionEvidence(&candidate, "page")
			candidate.SourceKind = activity.SourceSystemProxy
			candidate.SourceDisplayName = "ViberMate runtime"
			candidate.SourceRecognition = activity.SourceRecognitionUnknown
			candidate.ConnectionID = "connection-" + subject
		} else if kind == activity.KindEnvironmentApplied {
			candidate.EnvironmentID = "page-environment"
			candidate.EnvironmentRevision = 1
			candidate.EnvironmentDigest = strings.Repeat("b", 64)
		}
		record, err := repository.Append(context.Background(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
		return record
	}

	oldest := appendRecord(
		"activity-exchange-oldest",
		activity.KindExchangeCompleted,
		"exchange-oldest",
	)
	appendRecord(
		"activity-environment",
		activity.KindEnvironmentApplied,
		"environment-revision-1",
	)
	middle := appendRecord(
		"activity-exchange-middle",
		activity.KindExchangeCompleted,
		"exchange-middle",
	)
	appendRecord("activity-approval", activity.KindApprovalResolved, "approval-1")
	newest := appendRecord(
		"activity-exchange-newest",
		activity.KindExchangeCompleted,
		"exchange-newest",
	)

	exact, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Items) != 3 || exact.NextBeforeSequence != 0 {
		t.Fatalf("exact Exchange page = %+v", exact)
	}

	first, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 ||
		first.Items[0].ID != newest.ID ||
		first.Items[1].ID != middle.ID ||
		first.NextBeforeSequence != middle.Sequence {
		t.Fatalf("first Exchange page = %+v", first)
	}

	late := appendRecord(
		"activity-exchange-late",
		activity.KindExchangeCompleted,
		"exchange-late",
	)
	second, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{
			BeforeSequence: first.NextBeforeSequence,
			Limit:          2,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 ||
		second.Items[0].ID != oldest.ID ||
		second.NextBeforeSequence != 0 {
		t.Fatalf("second Exchange page = %+v", second)
	}
	raw, err := repository.List(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Items) != 6 {
		t.Fatalf("raw Activity page lost evidence: %+v", raw)
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
	recovered, err := reopened.ActivityRepository().ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.Items) != 4 ||
		recovered.Items[0].ID != late.ID ||
		recovered.Items[1].ID != newest.ID ||
		recovered.Items[2].ID != middle.ID ||
		recovered.Items[3].ID != oldest.ID ||
		recovered.Items[3].Sequence != oldest.Sequence {
		t.Fatalf("reopened Exchange page = %+v", recovered)
	}
	if recovered.Items[0].EnvironmentID != "page-environment" ||
		recovered.Items[0].ClientEndpointID != "page-endpoint" ||
		recovered.Items[0].ProtocolPlanID != "page-protocol" ||
		recovered.Items[0].RouteID != "page-route" ||
		recovered.Items[0].AccountID != "page-account" {
		t.Fatalf("reopened frozen execution evidence = %+v", recovered.Items[0])
	}
}

func TestActivityRepositoryFiltersExchangePagesByHalfOpenOccurrenceWindow(
	t *testing.T,
) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	appendExchange := func(id string, occurredAt time.Time) {
		t.Helper()
		record := activity.Record{
			ID: id, OccurredAt: occurredAt,
			Kind: activity.KindExchangeCompleted, SubjectID: "exchange-" + id,
			Status: activity.StatusSucceeded, SourceKind: activity.SourceSystemProxy,
			SourceDisplayName: "ViberMate runtime",
			SourceRecognition: activity.SourceRecognitionUnknown,
			ConnectionID:      "connection-" + id,
		}
		setFrozenExecutionEvidence(&record, "usage-window")
		if _, err := repository.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	from := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	appendExchange("before", from.Add(-time.Millisecond))
	appendExchange("from", from)
	appendExchange("inside", until.Add(-time.Millisecond))
	appendExchange("until", until)

	page, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{
			Limit: 10, OccurredAtOrAfter: from, OccurredBefore: until,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, item := range page.Items {
		got = append(got, item.ID)
	}
	if want := []string{"inside", "from"}; !slices.Equal(got, want) {
		t.Fatalf("windowed Exchanges = %v, want %v", got, want)
	}
}

func setFrozenExecutionEvidence(record *activity.Record, prefix string) {
	record.EnvironmentID = prefix + "-environment"
	record.EnvironmentRevision = 1
	record.EnvironmentDigest = strings.Repeat("a", 64)
	record.ClientEndpointID = prefix + "-endpoint"
	record.ClientEndpointRevision = 2
	record.ProtocolPlanID = prefix + "-protocol"
	record.ProtocolPlanRevision = 3
	record.RouteID = prefix + "-route"
	record.RouteRevision = 4
	record.AccountID = prefix + "-account"
	record.AccountRevision = 5
	record.CredentialEpoch = 6
	if record.Kind == activity.KindExchangeStarted {
		ref := agentconversation.Ref{
			ProjectionID: "exchange:" + record.SubjectID,
			Kind:         agentconversation.KindPendingExchange,
			Evidence:     agentconversation.EvidencePending,
		}
		record.Conversation = &ref
	} else if record.Kind == activity.KindExchangeCompleted {
		ref := agentconversation.Ref{
			ProjectionID: "exchange:" + record.SubjectID,
			Kind:         agentconversation.KindIsolatedExchange,
			Evidence:     agentconversation.EvidenceUndecodedExchange,
		}
		record.Conversation = &ref
	}
}

func TestActivityRepositoryFiltersByFrozenEnvironmentReference(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	appendExchange := func(identifier, prefix string) {
		t.Helper()
		record := activity.Record{
			ID:                identifier,
			OccurredAt:        time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
			Kind:              activity.KindExchangeCompleted,
			SubjectID:         "exchange-" + prefix,
			Status:            activity.StatusSucceeded,
			SourceKind:        activity.SourceSystemProxy,
			SourceDisplayName: "ViberMate runtime",
			SourceRecognition: activity.SourceRecognitionUnknown,
			ConnectionID:      "connection-" + prefix,
		}
		setFrozenExecutionEvidence(&record, prefix)
		if _, err := repository.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	appendExchange("activity-environment-a", "environment-a")
	appendExchange("activity-environment-b", "environment-b")

	page, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{
			Limit:         10,
			EnvironmentID: "environment-a-environment",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != "activity-environment-a" {
		t.Fatalf("filtered Environment page = %+v", page)
	}
}

func TestActivityRepositoryFiltersAndPaginatesByManualCapture(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	appendManual := func(identifier, exchangeID, manualCaptureID string) activity.Record {
		t.Helper()
		record := activity.Record{
			ID:                identifier,
			OccurredAt:        now,
			Kind:              activity.KindExchangeCompleted,
			SubjectID:         exchangeID,
			Status:            activity.StatusSucceeded,
			SourceKind:        activity.SourceManualProxy,
			SourceDisplayName: "Desktop proxy",
			SourceRecognition: activity.SourceRecognitionConfigured,
			ManualCaptureID:   manualCaptureID,
			ConnectionID:      "connection-" + exchangeID,
		}
		setFrozenExecutionEvidence(&record, exchangeID)
		stored, err := repository.Append(context.Background(), record)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
		return stored
	}
	oldest := appendManual("activity-manual-oldest", "exchange-oldest", "manual-one")
	appendManual("activity-manual-other", "exchange-other", "manual-two")
	newest := appendManual("activity-manual-newest", "exchange-newest", "manual-one")

	first, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{Limit: 1, ManualCaptureID: "manual-one"},
	)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != newest.ID ||
		first.NextBeforeSequence != newest.Sequence {
		t.Fatalf("first ManualCapture page = %+v, %v", first, err)
	}
	second, err := repository.ListExchanges(
		context.Background(),
		activity.PageRequest{
			BeforeSequence:  first.NextBeforeSequence,
			Limit:           1,
			ManualCaptureID: "manual-one",
		},
	)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != oldest.ID ||
		second.NextBeforeSequence != 0 {
		t.Fatalf("second ManualCapture page = %+v, %v", second, err)
	}
}

func TestExchangeDetailIndexesAreInstalled(t *testing.T) {
	t.Parallel()

	store := openTestStore(
		t,
		filepath.Join(t.TempDir(), "data", "runtime.db"),
	)
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{
		"runtime_activities_exchange_latest",
		"runtime_activities_exchange_capture_run_latest",
		"runtime_activities_exchange_manual_capture_latest",
		"runtime_activities_exchange_subject",
		"runtime_activities_exchange_conversation_latest",
		"runtime_egress_attempts_by_exchange",
	} {
		var count int
		if err := store.database.QueryRowContext(
			context.Background(),
			`SELECT count(*)
			 FROM sqlite_master
			 WHERE type = 'index'
			   AND name = ?`,
			name,
		).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("index %q count = %d, want 1", name, count)
		}
	}
}

func TestActivityRepositoryNeverMixesFlatAgentConversations(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	appendConversation := func(exchangeID, projectionID string) {
		record := activity.Record{
			ID:                "activity-" + exchangeID,
			OccurredAt:        time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC),
			Kind:              activity.KindExchangeCompleted,
			SubjectID:         exchangeID,
			Status:            activity.StatusSucceeded,
			SourceKind:        activity.SourceCaptureRun,
			SourceDisplayName: "claude",
			SourceRecognition: activity.SourceRecognitionVerified,
			CaptureRunID:      "run-agents",
			ConnectionID:      "connection-" + exchangeID,
		}
		setFrozenExecutionEvidence(&record, "agents")
		ref := agentconversation.Ref{
			ProjectionID: projectionID,
			Kind:         agentconversation.KindIsolatedSubagent,
			Evidence:     agentconversation.EvidenceClientAssertedSubagent,
		}
		record.Conversation = &ref
		if _, err := repository.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	appendConversation("exchange-review", "exchange:exchange-review")
	appendConversation("exchange-test", "exchange:exchange-test")

	page, err := repository.ListExchanges(context.Background(), activity.PageRequest{
		Limit: 10, CaptureRunID: "run-agents",
		ConversationProjectionID: "exchange:exchange-review",
	})
	if err != nil || len(page.Items) != 1 ||
		page.Items[0].SubjectID != "exchange-review" ||
		page.Items[0].Conversation == nil ||
		page.Items[0].Conversation.ProjectionID != "exchange:exchange-review" {
		t.Fatalf("filtered Agent Conversation = %+v, %v", page, err)
	}
}

func TestActivityRepositoryConversationIndexIsStableAndCounted(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	sequence := 0
	appendTurn := func(exchangeID, projectionID, displayName string) {
		sequence++
		record := activity.Record{
			ID:         "activity-" + exchangeID,
			OccurredAt: time.Date(2026, 8, 13, 9, sequence, 0, 0, time.UTC),
			Kind:       activity.KindExchangeCompleted, SubjectID: exchangeID,
			Status: activity.StatusSucceeded, SourceKind: activity.SourceCaptureRun,
			SourceDisplayName: "codex", SourceRecognition: activity.SourceRecognitionVerified,
			CaptureRunID: "run-index", ConnectionID: "connection-" + exchangeID,
		}
		setFrozenExecutionEvidence(&record, "index")
		ref := agentconversation.Ref{
			ProjectionID: projectionID, DisplayName: displayName,
			Kind:     agentconversation.KindAgent,
			Evidence: agentconversation.EvidenceExplicitActor,
			Actor:    "/root/" + displayName,
		}
		record.Conversation = &ref
		if _, err := repository.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("one", "capture_run:run-index:agent:a", "alpha")
	appendTurn("two", "capture_run:run-index:agent:b", "beta")
	appendTurn("three", "capture_run:run-index:agent:a", "alpha")

	first, err := repository.ListConversations(context.Background(), activity.ConversationIndexRequest{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].TurnCount != 1 ||
		first.Items[0].Latest.SubjectID != "two" || first.NextBeforeFirstSequence == 0 {
		t.Fatalf("first Conversation page = %+v, %v", first, err)
	}
	second, err := repository.ListConversations(context.Background(), activity.ConversationIndexRequest{
		BeforeFirstSequence: first.NextBeforeFirstSequence, Limit: 1,
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].TurnCount != 2 ||
		second.Items[0].Conversation.DisplayName != "alpha" || second.NextBeforeFirstSequence != 0 {
		t.Fatalf("second Conversation page = %+v, %v", second, err)
	}
}

func TestActivityRepositoryConversationIndexKeepsDeepestObservedName(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer shutdownTestStore(t, store)
	repository := store.ActivityRepository()
	const (
		projectionID = "capture_run:run-names:agent:reviewer"
		actorID      = "reviewer-opaque-id"
		friendlyName = "Angle A line-by-line scan"
	)
	appendTurn := func(exchangeID, displayName string, minute int) {
		t.Helper()
		record := activity.Record{
			ID:                "activity-" + exchangeID,
			OccurredAt:        time.Date(2026, 8, 15, 10, minute, 0, 0, time.UTC),
			Kind:              activity.KindExchangeCompleted,
			SubjectID:         exchangeID,
			Status:            activity.StatusSucceeded,
			SourceKind:        activity.SourceCaptureRun,
			SourceDisplayName: "claude",
			SourceRecognition: activity.SourceRecognitionVerified,
			CaptureRunID:      "run-names",
			ConnectionID:      "connection-" + exchangeID,
		}
		setFrozenExecutionEvidence(&record, exchangeID)
		record.Conversation = &agentconversation.Ref{
			ProjectionID: projectionID,
			DisplayName:  displayName,
			Kind:         agentconversation.KindAgent,
			Evidence:     agentconversation.EvidenceExplicitActor,
			Actor:        actorID,
		}
		if _, err := repository.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	appendTurn("named-first", friendlyName, 1)
	appendTurn("opaque-later", actorID, 2)

	page, err := repository.ListConversations(
		context.Background(),
		activity.ConversationIndexRequest{Limit: 10, CaptureRunID: "run-names"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].TurnCount != 2 ||
		page.Items[0].Latest.SubjectID != "opaque-later" ||
		page.Items[0].Conversation.DisplayName != friendlyName ||
		page.Items[0].Latest.Conversation == nil ||
		page.Items[0].Latest.Conversation.DisplayName != actorID {
		t.Fatalf("Conversation name projection = %+v", page)
	}
}

func TestActivityRepositoryConversationIndexFiltersByCaptureAuthority(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	appendConversation := func(
		exchangeID string,
		captureRunID string,
		manualCaptureID string,
		projectionID string,
	) {
		t.Helper()
		record := activity.Record{
			ID:                "activity-" + exchangeID,
			OccurredAt:        time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
			Kind:              activity.KindExchangeCompleted,
			SubjectID:         exchangeID,
			Status:            activity.StatusSucceeded,
			SourceKind:        activity.SourceCaptureRun,
			SourceDisplayName: "claude",
			SourceRecognition: activity.SourceRecognitionVerified,
			CaptureRunID:      captureRunID,
			ManualCaptureID:   manualCaptureID,
			ConnectionID:      "connection-" + exchangeID,
		}
		if manualCaptureID != "" {
			record.SourceKind = activity.SourceManualProxy
			record.CaptureRunID = ""
		}
		setFrozenExecutionEvidence(&record, "filter")
		ref := agentconversation.Ref{
			ProjectionID: projectionID,
			Kind:         agentconversation.KindMain,
			Evidence:     agentconversation.EvidenceCaptureRun,
		}
		if manualCaptureID != "" {
			ref.Kind = agentconversation.KindIsolatedExchange
			ref.Evidence = agentconversation.EvidenceUndecodedExchange
		}
		record.Conversation = &ref
		if _, err := repository.Append(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	appendConversation("run-one-main", "run-one", "", "capture_run:run-one:main")
	appendConversation("run-two-main", "run-two", "", "capture_run:run-two:main")
	appendConversation(
		"manual-one-exchange",
		"",
		"manual-one",
		"exchange:manual-one-exchange",
	)

	managed, err := repository.ListConversations(
		context.Background(),
		activity.ConversationIndexRequest{Limit: 10, CaptureRunID: "run-one"},
	)
	if err != nil || len(managed.Items) != 1 ||
		managed.Items[0].Latest.CaptureRunID != "run-one" {
		t.Fatalf("managed Capture Conversation page = %+v, %v", managed, err)
	}
	manual, err := repository.ListConversations(
		context.Background(),
		activity.ConversationIndexRequest{Limit: 10, ManualCaptureID: "manual-one"},
	)
	if err != nil || len(manual.Items) != 1 ||
		manual.Items[0].Latest.ManualCaptureID != "manual-one" {
		t.Fatalf("manual Capture Conversation page = %+v, %v", manual, err)
	}
}

func TestActivityRepositoryReprojectsPendingExchangeByExactSession(t *testing.T) {
	t.Parallel()
	store := openTestStore(t, filepath.Join(t.TempDir(), "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	repository := store.ActivityRepository()
	projectionWriter := store.ConversationProjectionWriter()
	pending, err := agentconversation.Pending("exchange-pending-session")
	if err != nil {
		t.Fatal(err)
	}
	record := activity.Record{
		ID:                "activity-pending-session",
		OccurredAt:        time.Date(2026, 8, 23, 15, 12, 38, 0, time.UTC),
		Kind:              activity.KindExchangeStarted,
		SubjectID:         "exchange-pending-session",
		Status:            activity.StatusPending,
		SourceKind:        activity.SourceCaptureRun,
		SourceDisplayName: "codex",
		SourceRecognition: activity.SourceRecognitionConfigured,
		CaptureRunID:      "capture-codex-session",
		ConnectionID:      "connection-codex-session",
		Conversation:      &pending,
	}
	setFrozenExecutionEvidence(&record, "pending-session")
	record.AccountID = ""
	record.AccountRevision = 0
	record.CredentialEpoch = 0
	if _, err := repository.Append(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	session := agentconversation.Ref{
		ProjectionID: "capture_run:capture-codex-session:session:opaque:main",
		DisplayName:  "codex",
		Kind:         agentconversation.KindMain,
		Evidence:     agentconversation.EvidenceExplicitSession,
	}
	if err := projectionWriter.ReprojectConversation(
		context.Background(),
		"exchange-pending-session",
		session,
	); err != nil {
		t.Fatalf("ReprojectConversation() error = %v", err)
	}
	page, err := repository.ListConversations(
		context.Background(),
		activity.ConversationIndexRequest{
			Limit:        10,
			CaptureRunID: "capture-codex-session",
		},
	)
	if err != nil || len(page.Items) != 1 ||
		page.Items[0].Conversation.ProjectionID != session.ProjectionID ||
		page.Items[0].TurnCount != 1 ||
		page.Items[0].Latest.Status != activity.StatusPending {
		t.Fatalf("pending Session Conversation = %+v, %v", page, err)
	}
}
