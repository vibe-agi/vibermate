package runtimepersistence

import (
	"context"
	"database/sql"
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

func TestEgressAttemptsFilterByConnectionParentAndExchange(t *testing.T) {
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
	page, err = repository.List(context.Background(), egressaudit.PageRequest{
		Limit:      10,
		ExchangeID: "exchange-egress-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].Attempt.ID() != "egress-b" {
		t.Fatalf("Exchange filter = %+v", page.Items)
	}
	if _, err := repository.List(context.Background(), egressaudit.PageRequest{
		Limit:      10,
		ExchangeID: "exchange-invalid\nsecret",
	}); err == nil {
		t.Fatal("an unsafe Exchange filter was accepted")
	}
}

func TestEgressAttemptRecoveryTerminalizesOnlyInterruptedAttemptsOnce(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	repository := store.EgressAttemptRepository()

	interrupted := providerAttempt(t, "egress-interrupted")
	interruptedRecord, err := repository.Append(context.Background(), interrupted)
	if err != nil {
		t.Fatal(err)
	}
	completed := providerAttempt(t, "egress-completed")
	completedRecord, err := repository.Append(context.Background(), completed)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := completed.Finish(egressaudit.TerminalInput{
		Outcome:     egressaudit.OutcomeCompleted,
		BytesOut:    7,
		BytesIn:     11,
		CompletedAt: completed.StartedAt().Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Complete(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}

	// The recovery clock may lag a persisted start after a wall-clock rollback.
	// The terminal timestamp clamps to StartedAt so the immutable Attempt remains
	// valid without rewriting its original time or any routing evidence.
	recoveryAt := interrupted.StartedAt().Add(-time.Hour)
	recovered, err := repository.Recover(context.Background(), recoveryAt)
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered attempts = %d, want 1", recovered)
	}
	if recoveredAgain, err := repository.Recover(
		context.Background(),
		recoveryAt.Add(time.Minute),
	); err != nil || recoveredAgain != 0 {
		t.Fatalf("second recovery = %d, %v; want 0, nil", recoveredAgain, err)
	}

	page, err := repository.List(
		context.Background(),
		egressaudit.PageRequest{Limit: 10},
	)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]egressaudit.Record, len(page.Items))
	for _, record := range page.Items {
		byID[record.Attempt.ID()] = record
	}
	restored := byID[interrupted.ID()]
	if restored.Sequence != interruptedRecord.Sequence ||
		!restored.Attempt.Terminal() ||
		restored.Attempt.Outcome() != egressaudit.OutcomeFailed ||
		restored.Attempt.ErrorClass() != egressaudit.RecoveryErrorClass ||
		!restored.Attempt.CompletedAt().Equal(interrupted.StartedAt()) ||
		restored.Attempt.BytesOut() != 0 ||
		restored.Attempt.BytesIn() != 0 ||
		restored.Attempt.Parent() != interrupted.Parent() ||
		restored.Attempt.Decision() != interrupted.Decision() {
		t.Fatalf("recovered attempt = %+v", restored)
	}
	preserved := byID[completed.ID()]
	if preserved.Sequence != completedRecord.Sequence ||
		preserved.Attempt.Outcome() != egressaudit.OutcomeCompleted ||
		preserved.Attempt.ErrorClass() != "" ||
		preserved.Attempt.BytesOut() != 7 ||
		preserved.Attempt.BytesIn() != 11 ||
		!preserved.Attempt.CompletedAt().Equal(terminal.CompletedAt()) {
		t.Fatalf("previous terminal was rewritten: %+v", preserved)
	}
}

func TestEgressAttemptRecoveryRejectsPartialTerminalAtomically(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	repository := store.EgressAttemptRepository()
	for _, id := range []string{"egress-eligible", "egress-partial"} {
		if _, err := repository.Append(
			context.Background(),
			providerAttempt(t, id),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.database.ExecContext(
		context.Background(),
		`UPDATE runtime_egress_attempts
		    SET completed_at_unix_ms = started_at_unix_ms
		  WHERE attempt_id = ?`,
		"egress-partial",
	); err != nil {
		t.Fatal(err)
	}

	if recovered, err := repository.Recover(
		context.Background(),
		time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC),
	); err == nil || recovered != 0 {
		t.Fatalf("partial terminal recovery = %d, %v; want failure", recovered, err)
	}
	var outcome string
	var completedAt any
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT outcome, completed_at_unix_ms
		   FROM runtime_egress_attempts
		  WHERE attempt_id = ?`,
		"egress-eligible",
	).Scan(&outcome, &completedAt); err != nil {
		t.Fatal(err)
	}
	if outcome != "" || completedAt != nil {
		t.Fatalf(
			"eligible record changed despite failed recovery: outcome=%q completed=%v",
			outcome,
			completedAt,
		)
	}
	if _, err := repository.List(
		context.Background(),
		egressaudit.PageRequest{Limit: 10},
	); err == nil {
		t.Fatal("a partial durable terminal was restored as ordinary evidence")
	}
}

func TestEgressAttemptRecoveryRejectsInvalidDurableEvidenceAtomically(
	t *testing.T,
) {
	t.Parallel()

	tests := []struct {
		name       string
		assignment string
	}{
		{
			name:       "completion without outcome",
			assignment: "completed_at_unix_ms = started_at_unix_ms",
		},
		{
			name:       "outcome without completion",
			assignment: "outcome = 'failed', error_class = 'transport_failed'",
		},
		{
			name:       "nonterminal error",
			assignment: "error_class = 'transport_failed'",
		},
		{
			name:       "nonterminal bytes out",
			assignment: "bytes_out = 1",
		},
		{
			name:       "nonterminal bytes in",
			assignment: "bytes_in = 1",
		},
		{
			name: "completion before start",
			assignment: "completed_at_unix_ms = started_at_unix_ms - 1, " +
				"outcome = 'completed'",
		},
		{
			name: "unsupported outcome",
			assignment: "completed_at_unix_ms = started_at_unix_ms, " +
				"outcome = 'invented'",
		},
		{
			name: "invalid error class",
			assignment: "completed_at_unix_ms = started_at_unix_ms, " +
				"outcome = 'failed', error_class = char(10)",
		},
		{
			name: "negative bytes out",
			assignment: "completed_at_unix_ms = started_at_unix_ms, " +
				"outcome = 'failed', error_class = 'transport_failed', bytes_out = -1",
		},
		{
			name: "negative bytes in",
			assignment: "completed_at_unix_ms = started_at_unix_ms, " +
				"outcome = 'failed', error_class = 'transport_failed', bytes_in = -1",
		},
		{
			name: "invalid reused flag",
			assignment: "completed_at_unix_ms = started_at_unix_ms, " +
				"outcome = 'completed', reused_transport = 2",
		},
		{
			name: "invalid policy revision",
			assignment: "completed_at_unix_ms = started_at_unix_ms, " +
				"outcome = 'completed', policy_revision = -1",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := openTestStore(
				t,
				filepath.Join(t.TempDir(), "data", "runtime.db"),
			)
			defer shutdownTestStore(t, store)
			repository := store.EgressAttemptRepository()
			for _, id := range []string{
				"egress-eligible",
				"egress-invalid-durable",
			} {
				if _, err := repository.Append(
					context.Background(),
					providerAttempt(t, id),
				); err != nil {
					t.Fatal(err)
				}
			}
			writeInvalidEgressAttemptFixture(
				t,
				store,
				test.assignment,
				"egress-invalid-durable",
			)
			invalidBefore := readDurableEgressTerminal(
				t,
				store,
				"egress-invalid-durable",
			)

			if recovered, err := repository.Recover(
				context.Background(),
				time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC),
			); err == nil || recovered != 0 {
				t.Fatalf(
					"invalid durable recovery = %d, %v; want failure",
					recovered,
					err,
				)
			}
			eligible := readDurableEgressTerminal(
				t,
				store,
				"egress-eligible",
			)
			if eligible.CompletedAt.Valid || eligible.Outcome != "" ||
				eligible.ErrorClass != "" || eligible.BytesOut != 0 ||
				eligible.BytesIn != 0 {
				t.Fatalf(
					"eligible record changed despite failed recovery: %+v",
					eligible,
				)
			}
			invalidAfter := readDurableEgressTerminal(
				t,
				store,
				"egress-invalid-durable",
			)
			if invalidAfter != invalidBefore {
				t.Fatalf(
					"recovery rewrote invalid prior evidence: before=%+v after=%+v",
					invalidBefore,
					invalidAfter,
				)
			}
			if _, err := repository.List(
				context.Background(),
				egressaudit.PageRequest{Limit: 10},
			); err == nil {
				t.Fatal("invalid durable evidence was restored as an Attempt")
			}
		})
	}
}

type durableEgressTerminal struct {
	CompletedAt sql.NullInt64
	Outcome     string
	ErrorClass  string
	BytesOut    int64
	BytesIn     int64
}

func readDurableEgressTerminal(
	t *testing.T,
	store *Store,
	attemptID string,
) durableEgressTerminal {
	t.Helper()

	var terminal durableEgressTerminal
	if err := store.database.QueryRowContext(
		context.Background(),
		`SELECT completed_at_unix_ms, outcome, error_class, bytes_out, bytes_in
		   FROM runtime_egress_attempts
		  WHERE attempt_id = ?`,
		attemptID,
	).Scan(
		&terminal.CompletedAt,
		&terminal.Outcome,
		&terminal.ErrorClass,
		&terminal.BytesOut,
		&terminal.BytesIn,
	); err != nil {
		t.Fatal(err)
	}
	return terminal
}

func writeInvalidEgressAttemptFixture(
	t *testing.T,
	store *Store,
	assignment string,
	attemptID string,
) {
	t.Helper()

	connection, err := store.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(
		context.Background(),
		`PRAGMA ignore_check_constraints = ON`,
	); err != nil {
		_ = connection.Close()
		t.Fatalf("enable invalid SQLite fixture: %v", err)
	}
	_, mutationErr := connection.ExecContext(
		context.Background(),
		"UPDATE runtime_egress_attempts SET "+assignment+" WHERE attempt_id = ?",
		attemptID,
	)
	_, resetErr := connection.ExecContext(
		context.Background(),
		`PRAGMA ignore_check_constraints = OFF`,
	)
	closeErr := connection.Close()
	if mutationErr != nil {
		t.Fatalf("write invalid SQLite fixture: %v", mutationErr)
	}
	if resetErr != nil {
		t.Fatalf("restore SQLite constraint enforcement: %v", resetErr)
	}
	if closeErr != nil {
		t.Fatalf("close invalid SQLite fixture connection: %v", closeErr)
	}
}

func TestEgressAttemptRecoveryHonorsCancellation(t *testing.T) {
	t.Parallel()

	store := openTestStore(t, filepath.Join(t.TempDir(), "data", "runtime.db"))
	defer func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("close store: %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if recovered, err := store.EgressAttemptRepository().Recover(
		ctx,
		time.Date(2026, 8, 2, 2, 0, 0, 0, time.UTC),
	); err == nil || recovered != 0 {
		t.Fatalf("canceled recovery = %d, %v; want failure", recovered, err)
	}
}
