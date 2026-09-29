// convert-v1 is an explicit, offline conversion of released 0.1.15/0.1.16 data
// shape. It is deliberately not linked into either Runtime executable.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/instanceguard"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

const releasedDigest = "94865976df1130b198b9dbe28da1a249a0082adf9e797f43cb96005904ec7914"
const released016Digest = "aca772a7d57e0a0e22584f7ab9427db9fbe5a57f5edf6f6ba722f212afb72897"

// These are derived indexes, not a second content authority. SQLite's current
// triggers build them during import; verify both directions before committing.
var derivedReferenceQueries = map[string]string{
	"runtime_exchange_content_block_refs": `WITH RECURSIVE spans(digest,position) AS (
	 SELECT digest,1 FROM runtime_exchange_content_messages UNION ALL
	 SELECT spans.digest,position+64 FROM spans JOIN runtime_exchange_content_messages m ON m.digest=spans.digest
	 WHERE position+64<=length(m.block_manifest)
	) SELECT spans.digest,substr(m.block_manifest,position,64) FROM spans JOIN runtime_exchange_content_messages m ON m.digest=spans.digest`,
	"runtime_evidence_chunk_refs": `WITH RECURSIVE spans(digest,position) AS (
	 SELECT digest,1 FROM runtime_evidence_bodies UNION ALL
	 SELECT spans.digest,position+32 FROM spans JOIN runtime_evidence_bodies b ON b.digest=spans.digest
	 WHERE position+32<=length(b.chunk_manifest)
	) SELECT spans.digest,substr(b.chunk_manifest,position,32) FROM spans JOIN runtime_evidence_bodies b ON b.digest=spans.digest`,
}

var releasedExtensions = map[string]string{
	"acp_schema_metadata":                 "422b6a07fe5bc02d430a792768929ef9345b9b82c4cc24ec933931e205fc50f6",
	"runtime_user_policy_schema_metadata": "cb04112805d29b06be905442cac8785faa4f0664db807f721575d465f0e83171",
	"capture_project_schema_metadata":     "0ac50a309e12b473bc73aecc4d7bb82fcd7bab138f4b0401f1d08b0c23b4fee1",
	"runtime_usage_schema_metadata":       "fb446b5c08e763ca7a1df7d6e6ba3284c2eae95cd614ce826c7e3dc9c7444a48",
}

// This abandoned extension never selected an account when its array was empty.
// Recognize only its exact shape, offline; any non-empty selection needs review.
var retiredSelectionTables = map[string]string{
	"capture_account_selection_schema_metadata": `CREATE TABLE capture_account_selection_schema_metadata(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  revision INTEGER NOT NULL CHECK(revision = 1),
  source_sha256 TEXT NOT NULL CHECK(length(source_sha256) = 64)
) STRICT`,
	"capture_account_selections": `CREATE TABLE capture_account_selections(
  capture_kind TEXT NOT NULL CHECK(capture_kind IN('managed_run', 'manual_capture')),
  capture_id TEXT NOT NULL CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  selections_json TEXT NOT NULL
  CHECK(json_valid(selections_json)
    AND json_type(selections_json) = 'array'
    AND json_array_length(selections_json) BETWEEN 0 AND 128),
  PRIMARY KEY(capture_kind, capture_id),
  FOREIGN KEY(capture_kind, capture_id)
  REFERENCES capture_environment_assignments(capture_kind, capture_id)
  ON DELETE CASCADE
) STRICT`,
}

type receipt struct {
	Schema         string           `json:"schema"`
	CreatedAt      time.Time        `json:"createdAt"`
	SourceSchema   string           `json:"sourceSchemaSha256"`
	TargetSchema   string           `json:"targetSchemaSha256"`
	BackupDatabase string           `json:"backupDatabaseSha256"`
	TargetDatabase string           `json:"targetDatabaseSha256"`
	Rows           map[string]int64 `json:"verifiedRows"`
	RetiredRows    map[string]int64 `json:"retiredEmptySelectionRows,omitempty"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	flags := flag.NewFlagSet("convert-v1", flag.ContinueOnError)
	source := flags.String("source", "", "stopped 0.1.15/0.1.16 Runtime directory (unchanged)")
	backup := flags.String("backup", "", "new private full-copy backup directory")
	target := flags.String("target", "", "new converted directory (not selected automatically)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	result, err := convert(ctx, *source, *backup, *target)
	if err != nil {
		fmt.Fprintln(os.Stderr, "conversion not completed; source is unchanged; do not select the target:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "conversion completed; read conversion.json in the target for verification")
		os.Exit(1)
	}
}

func convert(ctx context.Context, source, backup, target string) (receipt, error) {
	var result receipt
	if ctx == nil {
		return result, runtimedata.ErrTarget
	}
	// Validate all destinations before even making the backup. Copy owns the
	// real Runtime/server locks, byte verification, permissions and WAL handling.
	paths := []string{source, backup, target}
	for i, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
			return result, runtimedata.ErrTarget
		}
		resolved, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil || resolved != filepath.Dir(path) {
			return result, runtimedata.ErrTarget
		}
		if i > 0 {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return result, runtimedata.ErrTarget
			}
		}
		for _, other := range paths[:i] {
			if within(path, other) || within(other, path) {
				return result, runtimedata.ErrTarget
			}
		}
	}
	if err := runtimedata.Copy(ctx, source, backup); err != nil {
		return result, fmt.Errorf("full backup: %w", err)
	}
	// Copy owns the locks during the snapshot. Keep the original stopped during
	// conversion too; if it was restarted, leave the backup and refuse cutover.
	sourceGuard, err := runtimedata.Acquire(source)
	if err != nil {
		return result, err
	}
	defer sourceGuard.Release()
	serverGuard, err := instanceguard.Acquire(filepath.Join(source, "server.lock"))
	if err != nil {
		return result, err
	}
	defer serverGuard.Release()
	if err := runtimedata.Copy(ctx, backup, target); err != nil {
		return result, fmt.Errorf("working copy: %w", err)
	}
	backupGuard, err := runtimedata.Acquire(backup)
	if err != nil {
		return result, err
	}
	defer backupGuard.Release()
	guard, err := runtimedata.Acquire(target)
	if err != nil {
		return result, err
	}
	defer guard.Release()
	stage, err := os.MkdirTemp(target, ".convert-")
	if err != nil {
		return result, err
	}
	// Keep an incomplete stage on failure for inspection; it never replaces
	// the source or backup and has no conversion receipt claiming success.
	newPath := filepath.Join(stage, "runtime.db")
	store, err := openCurrent(ctx, newPath)
	if err != nil {
		return result, err
	}
	state, stateErr := store.SchemaStateReader().ReadSchemaState(ctx)
	if err := errors.Join(stateErr, store.Shutdown(ctx)); err != nil {
		return result, err
	}
	result = receipt{Schema: "vibermate.offline-conversion/v1", CreatedAt: time.Now().UTC(),
		TargetSchema: state.SourceSHA256, Rows: map[string]int64{}}
	if err := copyDatabase(ctx, filepath.Join(backup, "runtime.db"), newPath, &result); err != nil {
		return result, err
	}
	if _, err := runtimepersistence.ValidateOfflineDatabase(ctx, newPath); err != nil {
		return result, err
	}
	store, err = openCurrent(ctx, newPath)
	if err != nil {
		return result, err
	}
	_, accountsErr := store.ProviderAccountRepository().LoadAll(ctx)
	if err := errors.Join(accountsErr, store.Shutdown(ctx)); err != nil {
		return result, fmt.Errorf("validate current account records: %w", err)
	}
	result.BackupDatabase, err = fileDigest(filepath.Join(backup, "runtime.db"))
	if err != nil {
		return result, err
	}
	result.TargetDatabase, err = fileDigest(newPath)
	if err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// These are only disposable files in the newly created working copy. The
	// original directory and verified full backup are never replaced or deleted.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(target, "runtime.db"+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
	}
	if err := os.Rename(newPath, filepath.Join(target, "runtime.db")); err != nil {
		return result, err
	}
	if err := os.Remove(stage); err != nil {
		return result, err
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return result, err
	}
	file, err := os.OpenFile(filepath.Join(target, "conversion.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return result, err
	}
	_, writeErr := file.Write(append(data, '\n'))
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return result, err
	}
	directory, err := os.Open(target)
	if err != nil {
		return result, err
	}
	return result, errors.Join(directory.Sync(), directory.Close())
}

func openCurrent(ctx context.Context, path string) (*runtimepersistence.Store, error) {
	return runtimepersistence.Open(ctx, runtimepersistence.Options{DatabasePath: path,
		BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
}

func copyDatabase(ctx context.Context, oldPath, newPath string, result *receipt) error {
	u := url.URL{Scheme: "file", Path: newPath, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	u.Path, u.RawQuery = oldPath, "mode=ro"
	if _, err := db.ExecContext(ctx, `ATTACH DATABASE ? AS released`, u.String()); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `PRAGMA defer_foreign_keys=ON`); err != nil {
		return err
	}
	result.SourceSchema, err = checkReleased(ctx, tx)
	if err != nil {
		return err
	}
	tables, err := stringColumn(ctx, tx, `SELECT name FROM main.sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	tables = slices.DeleteFunc(tables, func(table string) bool {
		_, derived := derivedReferenceQueries[table]
		// Released 0.1.15/0.1.16 materialized retention in each observation;
		// they have no pending caps to copy. Only this new empty table is exempt.
		return derived || table == "runtime_usage_retention_caps"
	})
	oldTables, err := stringColumn(ctx, tx, `SELECT name FROM released.sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return err
	}
	from015 := result.SourceSchema == releasedDigest
	expected := slices.Clone(tables)
	if from015 {
		expected = append(expected, "capture_run_projects")
		for name := range releasedExtensions {
			expected = append(expected, name)
		}
		result.RetiredRows, err = checkRetiredSelections(ctx, tx, oldTables)
		if err != nil {
			return err
		}
		for name := range result.RetiredRows {
			expected = append(expected, name)
		}
	}
	slices.Sort(expected)
	if !slices.Equal(expected, oldTables) {
		return errors.New("unsupported source table inventory")
	}
	for _, table := range tables {
		if err := copyTable(ctx, tx, table, result.Rows, from015); err != nil {
			return fmt.Errorf("convert table %s: %w", table, err)
		}
	}
	result.Rows["runtime_usage_retention_caps"] = 0
	// Preserve AUTOINCREMENT high watermarks even if the highest rows expired.
	if _, err := tx.ExecContext(ctx, `UPDATE main.sqlite_sequence SET seq=max(seq,coalesce((SELECT seq FROM released.sqlite_sequence s WHERE s.name=main.sqlite_sequence.name),0));
	 INSERT INTO main.sqlite_sequence(name,seq) SELECT name,seq FROM released.sqlite_sequence s WHERE NOT EXISTS(SELECT 1 FROM main.sqlite_sequence n WHERE n.name=s.name)`); err != nil {
		return err
	}
	if err := validateJSON(ctx, tx); err != nil {
		return err
	}
	for table, expected := range derivedReferenceQueries {
		want := `SELECT * FROM (` + expected + `)`
		got := `SELECT * FROM main.` + identifier(table)
		for _, query := range []string{want + ` EXCEPT ` + got, got + ` EXCEPT ` + want} {
			var mismatch bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(`+query+`)`).Scan(&mismatch); err != nil || mismatch {
				return errors.Join(errors.New("derived content reference verification failed: "+table), err)
			}
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM main.`+identifier(table)).Scan(&count); err != nil {
			return err
		}
		result.Rows[table] = count
	}
	return tx.Commit()
}

func checkRetiredSelections(ctx context.Context, tx *sql.Tx, tables []string) (map[string]int64, error) {
	if !slices.Contains(tables, "capture_account_selection_schema_metadata") && !slices.Contains(tables, "capture_account_selections") {
		return nil, nil
	}
	counts := make(map[string]int64)
	for table, expected := range retiredSelectionTables {
		var definition string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM released.sqlite_schema WHERE type='table' AND name=?`, table).Scan(&definition); err != nil ||
			strings.Join(strings.Fields(definition), " ") != strings.Join(strings.Fields(expected), " ") {
			return nil, errors.New("unsupported retired selection table: " + table)
		}
		var count int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM released.`+identifier(table)).Scan(&count); err != nil {
			return nil, err
		}
		counts[table] = count
	}
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT singleton=1 AND revision=1 AND source_sha256='17798ddd22330b6ed9d43ae21d86b73929b39e02c06b1c059e652ea1c4e93edb' FROM released.capture_account_selection_schema_metadata`).Scan(&valid); err != nil || !valid || counts["capture_account_selection_schema_metadata"] != 1 {
		return nil, errors.New("unsupported retired selection metadata")
	}
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM released.capture_account_selections s
	 WHERE NOT json_valid(selections_json) OR json_type(selections_json)<>'array' OR json_array_length(selections_json)<>0
	 OR NOT EXISTS(SELECT 1 FROM released.capture_environment_assignments a WHERE a.capture_kind=s.capture_kind AND a.capture_id=s.capture_id))`).Scan(&valid); err != nil || !valid {
		return nil, errors.New("non-empty or invalid retired selections require review; nothing was discarded")
	}
	return counts, nil
}

func checkReleased(ctx context.Context, tx *sql.Tx) (string, error) {
	var identity, digest string
	var revision, count int
	if err := tx.QueryRowContext(ctx, `SELECT schema_identity,schema_revision,schema_source_sha256 FROM released.runtime_metadata WHERE singleton=1`).Scan(&identity, &revision, &digest); err != nil {
		return "", err
	}
	if identity != "vibermate-runtime-clean-baseline" || revision != 1 || (digest != releasedDigest && digest != released016Digest) {
		return "", errors.New("only the frozen 0.1.15/0.1.16 source baselines are supported")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM released.sqlite_schema WHERE type IN ('trigger','view')`).Scan(&count); err != nil || count != 0 {
		return "", errors.New("source has unrecognized executable schema objects")
	}
	if digest == released016Digest {
		return digest, nil
	}
	for table, expected := range releasedExtensions {
		var extensionDigest string
		if err := tx.QueryRowContext(ctx, `SELECT count(*),min(source_sha256) FROM released.`+identifier(table)).Scan(&count, &extensionDigest); err != nil || count != 1 || extensionDigest != expected {
			return "", errors.New("unsupported source extension: " + table)
		}
	}
	// Old Git evidence is local-clone evidence. Do not consult today's remote,
	// fabricate cross-machine identity, or overwrite a partially converted row.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM released.capture_run_projects WHERE json_type(git_json,'$.repositorySource') IS NOT NULL`).Scan(&count); err != nil || count != 0 {
		return "", errors.New("source Git snapshot is not the released shape")
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM released.runtime_usage_observations WHERE json_type(observation_json,'$.attribution.gitAtLaunch')='object' AND
	 (json_type(observation_json,'$.attribution.gitAtLaunch.repositorySource') IS NOT NULL OR
	  coalesce(json_extract(observation_json,'$.attribution.projectId'),'') NOT GLOB 'git:*' OR length(json_extract(observation_json,'$.attribution.projectId'))<>68)`).Scan(&count); err != nil || count != 0 {
		return "", errors.New("source usage Git attribution is not the released shape")
	}
	return digest, nil
}

func copyTable(ctx context.Context, tx *sql.Tx, table string, counts map[string]int64, from015 bool) error {
	columns, err := stringColumn(ctx, tx, `SELECT name FROM pragma_table_xinfo(?, 'main') WHERE hidden=0 ORDER BY cid`, table)
	if err != nil {
		return err
	}
	oldColumns, err := stringColumn(ctx, tx, `SELECT name FROM pragma_table_xinfo(?, 'released') WHERE hidden=0 ORDER BY cid`, table)
	if err != nil {
		return err
	}
	var names, selections, expectedOld []string
	for _, column := range columns {
		if from015 && table == "runtime_usage_observations" && column == "sequence" {
			continue // New stable local ordering; old exchange identities survive.
		}
		names = append(names, identifier(column))
		expression := "s." + identifier(column)
		if !from015 {
			expectedOld = append(expectedOld, column)
		} else {
			switch table + "." + column {
			case "capture_runs.git_json":
				expression = `coalesce((SELECT json_set(git_json,'$.repositorySource','local') FROM released.capture_run_projects p WHERE p.run_id=s.run_id),'null')`
			case "provider_accounts.settings_revision":
				expression = `1`
			case "provider_accounts.egress_profile_json":
				expression = `'{}'`
			case "provider_accounts.automatic_refresh":
				expression = `(s.driver_ref='codex_oauth')` // Preserve existing behavior; new imports remain opt-in.
			case "runtime_egress_attempts.proxy_revision", "runtime_egress_attempts.account_settings_revision":
				expression = `0` // Not observed historically, not inferred from current accounts.
			case "runtime_egress_attempts.account_id":
				expression = `''`
			default:
				expectedOld = append(expectedOld, column)
			}
		}
		if table == "runtime_metadata" && column == "schema_source_sha256" {
			expression = `(SELECT schema_source_sha256 FROM main.runtime_metadata WHERE singleton=1)`
		}
		if from015 && table == "runtime_usage_observations" && column == "observation_json" {
			expression = `CASE WHEN json_type(s.observation_json,'$.attribution.gitAtLaunch')='object' THEN
			 json_set(s.observation_json,'$.attribution.gitAtLaunch.repositorySource','local',
			 '$.attribution.projectId','git.local:'||substr(json_extract(s.observation_json,'$.attribution.projectId'),5)) ELSE s.observation_json END`
		}
		selections = append(selections, expression)
	}
	slices.Sort(expectedOld)
	slices.Sort(oldColumns)
	if !slices.Equal(expectedOld, oldColumns) {
		return errors.New("unsupported source columns")
	}
	quoted := identifier(table)
	if table == "runtime_metadata" {
		_, err := tx.ExecContext(ctx, `UPDATE main.runtime_metadata SET initialized_at=(SELECT initialized_at FROM released.runtime_metadata WHERE singleton=1)`)
		if err != nil {
			return err
		}
	} else {
		if table == "runtime_usage_policy" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM main.runtime_usage_policy`); err != nil {
				return err
			}
		}
		order := ""
		if table == "runtime_usage_observations" {
			order = " ORDER BY s.occurred_at_unix_ms,s.exchange_id"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO main.`+quoted+` (`+strings.Join(names, ",")+`) SELECT `+strings.Join(selections, ",")+` FROM released.`+quoted+` s`+order); err != nil {
			return err
		}
	}
	var before, after int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM released.`+quoted+`),(SELECT count(*) FROM main.`+quoted+`)`).Scan(&before, &after); err != nil || before != after {
		return errors.New("row count verification failed")
	}
	// Compare every preserved value, not just counts. Generated columns and new
	// usage ordering are derived; frozen payloads/credentials/permissions are not.
	want := `SELECT ` + strings.Join(selections, ",") + ` FROM released.` + quoted + ` s`
	got := `SELECT ` + strings.Join(names, ",") + ` FROM main.` + quoted
	for _, query := range []string{want + ` EXCEPT ` + got, got + ` EXCEPT ` + want} {
		var mismatch bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(`+query+`)`).Scan(&mismatch); err != nil || mismatch {
			return errors.New("record value verification failed")
		}
	}
	counts[table] = after
	return nil
}

func validateJSON(ctx context.Context, tx *sql.Tx) error {
	for _, query := range []string{`SELECT git_json FROM main.capture_runs`, `SELECT observation_json FROM main.runtime_usage_observations`} {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		for rows.Next() {
			var data []byte
			if err := rows.Scan(&data); err != nil {
				rows.Close()
				return err
			}
			if strings.Contains(query, "git_json") {
				var git *capturerun.GitSnapshot
				err = json.Unmarshal(data, &git)
				if err == nil && git != nil {
					err = git.Validate()
				}
			} else {
				var observation runtimeusage.Observation
				err = json.Unmarshal(data, &observation)
				if err == nil {
					err = observation.Validate()
				}
			}
			if err != nil {
				rows.Close()
				return errors.New("converted Git/usage record failed current validation")
			}
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
	}
	return nil
}

func stringColumn(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func identifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }
func within(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}
func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
