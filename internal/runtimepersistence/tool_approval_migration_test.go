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
	"github.com/vibe-agi/vibermate/internal/toolapproval"
)

func TestClientRootApprovalMigrationPreservesRowsAndWidensKind(t *testing.T) {
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
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 22); err != nil {
		t.Fatalf("create schema revision 22 fixture: %v", err)
	}
	created := time.Unix(1_780_000_000, 0).UTC()
	_, err = database.ExecContext(
		context.Background(),
		`INSERT INTO tool_approvals (
		     approval_id, revision, kind, subject_refs_json,
		     subject_labels_json, target_host, target_port, aggregate_key,
		     state, created_at_unix_ms, expires_at_unix_ms
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"approval-before-client-root",
		1,
		string(toolapproval.KindNetworkAsk),
		[]byte(`["api.example.com:443"]`),
		[]byte(`["api.example.com"]`),
		"api.example.com",
		443,
		"aggregate-before-client-root",
		string(toolapproval.StatePending),
		toUnixMillis(created),
		toUnixMillis(created.Add(time.Minute)),
	)
	if err != nil {
		t.Fatalf("insert schema revision 22 approval: %v", err)
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
	preserved, err := store.ToolApprovalRepository().Get(
		context.Background(),
		"approval-before-client-root",
	)
	if err != nil {
		t.Fatalf("read approval preserved by migration: %v", err)
	}
	if preserved.Kind != toolapproval.KindNetworkAsk ||
		preserved.Target.Host != "api.example.com" ||
		preserved.Target.Port != 443 {
		t.Fatalf("preserved approval = %+v", preserved)
	}

	clientRoot := toolapproval.Record{
		ID:            "approval-client-root-after-upgrade",
		Revision:      1,
		Kind:          toolapproval.KindClientRootAsk,
		AggregateKey:  "aggregate-client-root-after-upgrade",
		SubjectRefs:   []string{"publisher.example"},
		SubjectLabels: []string{"Example Publisher"},
		RequestCount:  1,
		WaiterCount:   1,
		State:         toolapproval.StatePending,
		CreatedAt:     created,
		ExpiresAt:     created.Add(time.Minute),
	}
	if err := store.ToolApprovalRepository().Create(
		context.Background(),
		clientRoot,
	); err != nil {
		t.Fatalf("create client Root approval after migration: %v", err)
	}
	stored, err := store.ToolApprovalRepository().Get(
		context.Background(),
		clientRoot.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Kind != toolapproval.KindClientRootAsk ||
		len(stored.SubjectLabels) != 1 ||
		stored.SubjectLabels[0] != "Example Publisher" {
		t.Fatalf("stored client Root approval = %+v", stored)
	}
}
