package runtimepersistence

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func providerAttempt(t *testing.T, id string) egressaudit.Attempt {
	t.Helper()

	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           id,
		ConnectionID: "connection-1",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "upstream-" + id,
			ExchangeID: "exchange-" + id,
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://provider.example:443",
		Decision: egressaudit.DecisionRef{
			PolicyID:       "policy-1",
			PolicyRevision: 1,
			Authority:      egressaudit.AuthorityAccess,
			RuleID:         "rule-1",
			ProxyID:        "direct",
		},
		StartedAt: time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}

func TestEgressAttemptsPersistAndSurviveReopen(t *testing.T) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	store := openTestStore(t, databasePath)
	repository := store.EgressAttemptRepository()

	first := providerAttempt(t, "egress-1")
	if _, err := repository.Append(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	terminal, err := first.Finish(egressaudit.TerminalInput{
		Outcome:     egressaudit.OutcomeCompleted,
		BytesOut:    128,
		BytesIn:     4096,
		CompletedAt: time.Date(2026, 8, 2, 1, 2, 5, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Complete(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(
		context.Background(),
		providerAttempt(t, "egress-2"),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	page, err := reopened.EgressAttemptRepository().List(
		context.Background(),
		egressaudit.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("persisted attempts = %d", len(page.Items))
	}
	var completed egressaudit.Attempt
	for _, record := range page.Items {
		if record.Attempt.ID() == "egress-1" {
			completed = record.Attempt
		}
	}
	if !completed.Terminal() ||
		completed.Outcome() != egressaudit.OutcomeCompleted ||
		completed.BytesIn() != 4096 ||
		completed.Parent().ExchangeID != "exchange-egress-1" ||
		completed.PayloadClass() != egressaudit.PayloadClientSemantic {
		t.Fatalf("restored attempt = %+v", completed)
	}
}

// One logical outbound writes one record. Pool reuse writes another marked
// record rather than overwriting the first, so an earlier destination is never
// rewritten.
func TestEgressAttemptIdentityIsUniqueAndReuseIsSeparate(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	repository := store.EgressAttemptRepository()

	attempt := providerAttempt(t, "egress-unique")
	if _, err := repository.Append(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(context.Background(), attempt); err == nil {
		t.Fatal("a duplicate egress identity was persisted")
	}

	reuse, err := egressaudit.New(egressaudit.NewInput{
		ID:           "egress-reused",
		ConnectionID: "connection-1",
		Purpose:      egressaudit.PurposeProviderAttempt,
		PayloadClass: egressaudit.PayloadClientSemantic,
		Parent: egressaudit.ParentRef{
			Kind:       egressaudit.ParentUpstreamAttempt,
			ID:         "upstream-2",
			ExchangeID: "exchange-2",
		},
		Caller:          egressaudit.CallerCore,
		TargetOrigin:    "https://provider.example:443",
		ReusedTransport: true,
		Decision: egressaudit.DecisionRef{
			PolicyID:       "policy-1",
			PolicyRevision: 1,
			Authority:      egressaudit.AuthorityAccess,
			RuleID:         "rule-1",
			ProxyID:        "direct",
		},
		StartedAt: time.Date(2026, 8, 2, 1, 3, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Append(context.Background(), reuse); err != nil {
		t.Fatal(err)
	}
	page, err := repository.List(
		context.Background(),
		egressaudit.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("attempts = %d, want both recorded", len(page.Items))
	}
	found := false
	for _, record := range page.Items {
		if record.Attempt.ID() == "egress-reused" &&
			record.Attempt.ReusedTransport() {
			found = true
		}
	}
	if !found {
		t.Fatal("pool reuse was not recorded as its own marked attempt")
	}
}

func TestEgressAttemptsFilterByConnectionAndParent(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	repository := store.EgressAttemptRepository()
	for _, id := range []string{"egress-a", "egress-b"} {
		if _, err := repository.Append(
			context.Background(),
			providerAttempt(t, id),
		); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.List(context.Background(), egressaudit.PageRequest{
		Limit:      10,
		ParentKind: egressaudit.ParentUpstreamAttempt,
		ParentID:   "upstream-egress-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Attempt.ID() != "egress-a" {
		t.Fatalf("parent filter = %+v", page.Items)
	}
	page, err = repository.List(context.Background(), egressaudit.PageRequest{
		Limit:        10,
		ConnectionID: "connection-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("connection filter = %+v", page.Items)
	}
}
