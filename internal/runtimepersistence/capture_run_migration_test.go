package runtimepersistence

import (
	"bytes"
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
	"github.com/vibe-agi/vibermate/internal/capturerun"
)

func TestCaptureRunClientEvidenceMigrationPreservesGenericRuns(
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
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(context.Background(), 11); err != nil {
		t.Fatalf("create schema revision 11 fixture: %v", err)
	}
	var proxyDigest capturerun.CapabilityDigest
	var controlDigest capturerun.CapabilityDigest
	copy(proxyDigest[:], bytes.Repeat([]byte{0x31}, 32))
	copy(controlDigest[:], bytes.Repeat([]byte{0x32}, 32))
	now := time.Date(2026, 7, 31, 1, 0, 0, 0, time.UTC)
	_, err = database.ExecContext(
		context.Background(),
		`INSERT INTO capture_runs (
		     run_id,
		     proxy_capability_hash,
		     control_capability_hash,
		     cwd,
		     executable_label,
		     process_id,
		     state,
		     created_at_unix_ms,
		     expires_at_unix_ms,
		     updated_at_unix_ms
		 )
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run-before-client-evidence",
		proxyDigest[:],
		controlDigest[:],
		"/tmp/vibermate-workspace",
		"unknown-agent",
		0,
		"created",
		toUnixMillis(now),
		toUnixMillis(now.Add(time.Minute)),
		toUnixMillis(now),
	)
	if err != nil {
		t.Fatalf("insert schema revision 11 CaptureRun: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	store := openTestStore(t, databasePath)
	defer shutdownTestStore(t, store)
	record, err := store.CaptureRunRepository().AuthorizeProxy(
		context.Background(),
		proxyDigest,
		now,
	)
	if err != nil {
		t.Fatalf("authorize migrated generic CaptureRun: %v", err)
	}
	if record.CatalogRevision != 1 || record.Adapter != nil {
		t.Fatalf("migrated generic CaptureRun = %+v", record)
	}
}
