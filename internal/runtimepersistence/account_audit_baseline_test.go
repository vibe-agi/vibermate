package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/egressaudit"
)

func TestUnreleasedAccountAuditCorrectionPreservesEvidenceAndSequence(t *testing.T) {
	for _, retainRows := range []bool{false, true} {
		t.Run(fmt.Sprintf("retain_rows_%t", retainRows), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "runtime.db")
			store := openTestStore(t, path)
			if _, err := store.EgressAttemptRepository().Append(ctx, providerAttempt(t, "retained")); err != nil {
				t.Fatal(err)
			}
			if err := store.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			db := sql.OpenDB(newSQLiteConnector(path, DefaultBusyTimeout))
			priorSchema := strings.ReplaceAll(schemaSQL, "'upstream_account_read',\n", "")
			priorSchema = strings.Replace(priorSchema, accountNoteColumnsSQL, "", 1)
			priorSchema = strings.ReplaceAll(priorSchema, "'credential_refresh',\n", "")
			if fmt.Sprintf("%x", sha256.Sum256([]byte(priorSchema))) != independentAccountDevelopmentDigest {
				t.Fatal("previous development fixture drifted")
			}
			_, definition, _ := strings.Cut(priorSchema, "CREATE TABLE runtime_egress_attempts(")
			definition, _, _ = strings.Cut(definition, ") STRICT;")
			if _, err := db.Exec(`ALTER TABLE runtime_egress_attempts RENAME TO fixture_rows;
                CREATE TABLE runtime_egress_attempts(` + definition + `) STRICT;
                INSERT INTO runtime_egress_attempts SELECT * FROM fixture_rows;
                DROP TABLE fixture_rows;`); err != nil {
				t.Fatal(err)
			}
			if !retainRows {
				if _, err := db.Exec(`DELETE FROM runtime_egress_attempts`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE sqlite_sequence SET seq = 900 WHERE name = 'runtime_egress_attempts'`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`ALTER TABLE provider_accounts DROP COLUMN note;
			ALTER TABLE provider_accounts DROP COLUMN note_revision;`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE runtime_metadata SET schema_source_sha256 = ?`, independentAccountDevelopmentDigest); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			current := openTestStore(t, path)
			defer current.Shutdown(ctx)
			page, err := current.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{})
			expected := 0
			if retainRows {
				expected = 1
			}
			if err != nil || len(page.Items) != expected {
				t.Fatalf("retained audit changed: %v", err)
			}
			if retainRows && (page.Items[0].Attempt.ID() != "retained" || page.Items[0].Sequence != 1) {
				t.Fatal("audit identity changed")
			}
			record, err := current.EgressAttemptRepository().Append(ctx, providerAttempt(t, "next"))
			if err != nil || record.Sequence != 901 {
				t.Fatalf("lost high-water sequence: %d %v", record.Sequence, err)
			}
			for _, purpose := range []string{"upstream_account_read", "credential_refresh"} {
				if _, err := current.database.ExecContext(ctx, `UPDATE runtime_egress_attempts SET purpose = ? WHERE attempt_id = 'next'`, purpose); err != nil {
					t.Fatal(err)
				}
			}
			var indexes int
			if err := current.database.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type = 'index' AND name LIKE 'runtime_egress_attempts_%'`).Scan(&indexes); err != nil || indexes != 4 {
				t.Fatalf("lost indexes: %d %v", indexes, err)
			}
		})
	}
}
