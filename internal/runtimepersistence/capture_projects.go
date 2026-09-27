package runtimepersistence

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// An additive extension leaves released Capture rows and their base digest
// intact. Deleting a Capture also deletes its optional launch snapshot.
const captureProjectSchema = `
CREATE TABLE capture_project_schema_metadata(singleton INTEGER PRIMARY KEY CHECK(singleton=1), source_sha256 TEXT NOT NULL) STRICT;
CREATE TABLE capture_run_projects(
 run_id TEXT PRIMARY KEY NOT NULL REFERENCES capture_runs(run_id) ON DELETE CASCADE,
 git_json TEXT NOT NULL CHECK(json_valid(git_json) AND length(git_json)<=2048)
) STRICT;
`

func initializeCaptureProjectSchema(ctx context.Context, database *sql.DB) error {
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	digest := sha256.Sum256([]byte(captureProjectSchema))
	expected := hex.EncodeToString(digest[:])
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name='capture_project_schema_metadata')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, captureProjectSchema); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO capture_project_schema_metadata VALUES(1,?)`, expected); err != nil {
			return err
		}
	} else {
		var actual string
		if err := tx.QueryRowContext(ctx, `SELECT source_sha256 FROM capture_project_schema_metadata WHERE singleton=1`).Scan(&actual); err != nil || actual != expected {
			return errors.New("unsupported Capture project schema extension")
		}
	}
	return tx.Commit()
}
