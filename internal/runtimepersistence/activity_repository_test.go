package runtimepersistence

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
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
		"runtime_activities_exchange_subject",
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
