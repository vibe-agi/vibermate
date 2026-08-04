package runtimepersistence

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func TestPluginEgressPurposeMigrationPreservesRowsAndWidensPurpose(
	t *testing.T,
) {
	t.Parallel()

	databasePath := filepath.Join(t.TempDir(), "data", "runtime.db")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	database := sql.OpenDB(newSQLiteConnector(
		databasePath,
		DefaultBusyTimeout,
	))
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	migrations, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectSQLite3,
		database,
		migrations,
		goose.WithSlog(slog.New(slog.NewTextHandler(io.Discard, nil))),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 25); err != nil {
		t.Fatalf("create schema revision 25 fixture: %v", err)
	}
	startedAt := time.Date(2026, 8, 2, 1, 2, 3, 0, time.UTC)
	_, err = database.ExecContext(
		context.Background(),
		`INSERT INTO runtime_egress_attempts (
		     sequence,
		     attempt_id,
		     connection_id,
		     purpose,
		     payload_class,
		     parent_kind,
		     parent_id,
		     parent_exchange_id,
		     caller_kind,
		     target_origin,
		     policy_id,
		     policy_revision,
		     policy_authority,
		     rule_id,
		     proxy_id,
		     reused_transport,
		     started_at_unix_ms,
		     completed_at_unix_ms,
		     outcome,
		     bytes_out,
		     bytes_in
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		41,
		"egress-before-plugin-purposes",
		"connection-before-plugin-purposes",
		string(egressaudit.PurposeProviderAttempt),
		string(egressaudit.PayloadClientSemantic),
		string(egressaudit.ParentUpstreamAttempt),
		"upstream-before-plugin-purposes",
		"exchange-before-plugin-purposes",
		string(egressaudit.CallerCore),
		"https://provider.example:443",
		egressaudit.BuiltInDirectPolicyID,
		1,
		string(egressaudit.AuthorityAccess),
		egressaudit.BuiltInDirectRuleID,
		egressaudit.BuiltInDirectProxyID,
		1,
		toUnixMillis(startedAt),
		toUnixMillis(startedAt.Add(2*time.Second)),
		string(egressaudit.OutcomeCompleted),
		17,
		23,
	)
	if err != nil {
		t.Fatalf("insert schema revision 25 EgressAttempt: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, databasePath)
	defer shutdownTestStore(t, store)
	state, err := store.SchemaStateReader().ReadSchemaState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 27 {
		t.Fatalf("upgraded schema revision = %d, want 27", state.Revision)
	}

	preservedPage, err := store.EgressAttemptRepository().List(
		context.Background(),
		egressaudit.PageRequest{
			Limit:   10,
			Purpose: egressaudit.PurposeProviderAttempt,
		},
	)
	if err != nil {
		t.Fatalf("list EgressAttempts preserved by migration: %v", err)
	}
	if len(preservedPage.Items) != 1 {
		t.Fatalf("preserved EgressAttempts = %+v", preservedPage.Items)
	}
	preserved := preservedPage.Items[0]
	if preserved.Sequence != 41 ||
		preserved.Attempt.ID() != "egress-before-plugin-purposes" ||
		!preserved.Attempt.Terminal() ||
		preserved.Attempt.Outcome() != egressaudit.OutcomeCompleted ||
		preserved.Attempt.BytesOut() != 17 ||
		preserved.Attempt.BytesIn() != 23 ||
		!preserved.Attempt.ReusedTransport() ||
		!preserved.Attempt.CompletedAt().Equal(startedAt.Add(2*time.Second)) {
		t.Fatalf("preserved EgressAttempt = %+v", preserved)
	}

	for index, purpose := range []egressaudit.EgressPurpose{
		egressaudit.PurposePluginCatalogSync,
		egressaudit.PurposePluginArtifactFetch,
	} {
		attempt := pluginDistributionAttempt(t, index, purpose)
		record, err := store.EgressAttemptRepository().Append(
			context.Background(),
			attempt,
		)
		if err != nil {
			t.Fatalf("append %q after migration: %v", purpose, err)
		}
		if record.Sequence != int64(42+index) {
			t.Fatalf(
				"%q sequence = %d, want %d",
				purpose,
				record.Sequence,
				42+index,
			)
		}
		page, err := store.EgressAttemptRepository().List(
			context.Background(),
			egressaudit.PageRequest{Limit: 10, Purpose: purpose},
		)
		if err != nil {
			t.Fatalf("list %q after migration: %v", purpose, err)
		}
		if len(page.Items) != 1 || page.Items[0].Attempt.ID() != attempt.ID() {
			t.Fatalf("listed %q attempts = %+v", purpose, page.Items)
		}
	}

	if _, err := store.database.ExecContext(
		context.Background(),
		`UPDATE runtime_egress_attempts
		    SET purpose = 'invented'
		  WHERE attempt_id = ?`,
		"egress-before-plugin-purposes",
	); err == nil {
		t.Fatal("the rebuilt purpose constraint accepted an unknown purpose")
	}
}

func pluginDistributionAttempt(
	t *testing.T,
	index int,
	purpose egressaudit.EgressPurpose,
) egressaudit.Attempt {
	t.Helper()

	suffix := "catalog"
	if purpose == egressaudit.PurposePluginArtifactFetch {
		suffix = "artifact"
	}
	attempt, err := egressaudit.New(egressaudit.NewInput{
		ID:           "egress-plugin-" + suffix,
		Purpose:      purpose,
		PayloadClass: egressaudit.PayloadRuntime,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentRuntimeAction,
			ID:   "runtime-distribution-" + suffix,
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: "https://plugins.example:443",
		Decision: egressaudit.BuiltInDirectDecision(
			egressaudit.AuthorityRuntime,
		),
		StartedAt: time.Date(2026, 8, 2, 2, index, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	return attempt
}
