package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/runtimedata"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

//go:embed testdata/released.sql
var releasedSQL string

func TestOfflineConversionPreservesDataAndUsesOnlyCurrentSchema(t *testing.T) {
	source, backup, target := fixture(t)
	before, _ := fileDigest(filepath.Join(source, "runtime.db"))
	ctx := context.Background()
	result, err := convert(ctx, source, backup, target)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rows["capture_runs"] != 1 || result.Rows["provider_accounts"] != 2 ||
		result.Rows["runtime_usage_observations"] != 2 || result.Rows["acp_observations"] != 1 ||
		result.Rows["runtime_user_policies"] != 1 || result.SourceSchema == result.TargetSchema {
		t.Fatalf("incomplete conversion: %+v", result)
	}
	for _, directory := range []string{source, backup} {
		if digest, _ := fileDigest(filepath.Join(directory, "runtime.db")); digest != before {
			t.Fatal("source or backup database changed")
		}
	}
	for _, directory := range []string{backup, target} {
		for _, name := range []string{"server-secrets/fixture", "local-ca/root-key.pem", "runtime-config.json"} {
			data, err := os.ReadFile(filepath.Join(directory, name))
			if err != nil || string(data) != "synthetic-sensitive-fixture" {
				t.Fatal("lost data file", name, err)
			}
			info, _ := os.Stat(filepath.Join(directory, name))
			if info.Mode().Perm() != 0o600 {
				t.Fatal("copy was not private", name)
			}
		}
	}
	data, err := os.ReadFile(filepath.Join(target, "conversion.json"))
	if err != nil || strings.Contains(string(data), "synthetic-sensitive") {
		t.Fatal("missing or sensitive receipt", err)
	}
	if _, err := openCurrent(ctx, filepath.Join(source, "runtime.db")); !errors.Is(err, runtimepersistence.ErrSchemaBaselineMismatch) {
		t.Fatal("runtime accepted old schema", err)
	}
	store, err := openCurrent(ctx, filepath.Join(target, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := store.ProviderAccountRepository().LoadAll(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatal("current account reader", err)
	}
	for _, account := range accounts {
		if account.SettingsRevision != 1 || account.EgressProfile.ID != "" ||
			account.AutomaticRefresh != (account.ID == "oauth") ||
			account.SecretRef.String() != "secret://provider-account/"+account.ID.String() || account.Note != "keep note" {
			t.Fatal("changed account meaning", account.ID)
		}
	}
	page, err := store.EgressAttemptRepository().List(ctx, egressaudit.PageRequest{Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].Attempt.Decision().AccountID != "" ||
		page.Items[0].Attempt.Decision().ProxyRevision != 0 {
		t.Fatal("invented historical route/account evidence", err)
	}
	if err := store.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	db := openFixtureDB(t, target)
	defer db.Close()
	var count, sequence, refresh int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE '%schema_metadata' OR name='capture_run_projects'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("old extensions remain", count, err)
	}
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name='runtime_egress_attempts'`).Scan(&sequence); err != nil || sequence != 700 {
		t.Fatal("lost high watermark", sequence, err)
	}
	var gitJSON string
	if err := db.QueryRow(`SELECT git_json FROM capture_runs`).Scan(&gitJSON); err != nil {
		t.Fatal(err)
	}
	var git capturerun.GitSnapshot
	if json.Unmarshal([]byte(gitJSON), &git) != nil || git.Validate() != nil || git.RepositorySource != "local" || git.Branch != "main" {
		t.Fatal("wrong historical Git conversion")
	}
	var payload string
	if err := db.QueryRow(`SELECT observation_json FROM runtime_usage_observations WHERE exchange_id='known'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var observation runtimeusage.Observation
	if json.Unmarshal([]byte(payload), &observation) != nil || observation.Validate() != nil ||
		observation.Attribution.ProjectID != "git.local:"+strings.Repeat("b", 64) ||
		observation.Usage.InputUncached.Tokens != 123 || observation.Usage.Output.Known {
		t.Fatal("usage tokens, unknowns or attribution changed")
	}
	if err := db.QueryRow(`SELECT enabled FROM runtime_usage_policy`).Scan(&refresh); err != nil || refresh != 1 {
		t.Fatal("collection consent changed", err)
	}
	var policy []byte
	if err := db.QueryRow(`SELECT allowed_environment_ids_json FROM runtime_user_policies`).Scan(&policy); err != nil || string(policy) != `["limited"]` {
		t.Fatal("permissions changed", err)
	}
	if digest, _ := fileDigest(filepath.Join(source, "runtime.db")); digest != before {
		t.Fatal("runtime rejection modified source")
	}
}

func TestOfflineConversionRejectsUnsupportedOrUnsafeInputsWithoutTouchingSource(t *testing.T) {
	for _, scenario := range []string{"busy", "target_exists", "backup_exists", "nested", "symlink", "canceled", "schema", "extension", "extra_table", "extra_column", "trigger", "invalid_usage", "partial_git"} {
		t.Run(scenario, func(t *testing.T) {
			source, backup, target := fixture(t)
			ctx := context.Background()
			switch scenario {
			case "busy":
				guard, err := runtimedata.Acquire(source)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Release()
			case "target_exists":
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "backup_exists":
				if err := os.Mkdir(backup, 0o700); err != nil {
					t.Fatal(err)
				}
			case "nested":
				target = filepath.Join(source, "nested")
			case "symlink":
				link := filepath.Join(filepath.Dir(source), "linked")
				if err := os.Symlink(source, link); err != nil {
					t.Fatal(err)
				}
				source = link
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			default:
				db := openFixtureDB(t, source)
				statements := map[string]string{
					"schema":        `UPDATE runtime_metadata SET schema_source_sha256=printf('%064d',0)`,
					"extension":     `UPDATE acp_schema_metadata SET source_sha256='wrong'`,
					"extra_table":   `CREATE TABLE unexpected_data(value TEXT); INSERT INTO unexpected_data VALUES('must not discard')`,
					"extra_column":  `ALTER TABLE provider_accounts ADD COLUMN surprise TEXT`,
					"trigger":       `CREATE TRIGGER surprise AFTER INSERT ON provider_accounts BEGIN SELECT 1; END`,
					"invalid_usage": `UPDATE runtime_usage_observations SET observation_json=json_set(observation_json,'$.usage.InputUncached.Tokens',-1)`,
					"partial_git":   `UPDATE capture_run_projects SET git_json=json_set(git_json,'$.repositorySource','remote')`,
				}
				if _, err := db.Exec(statements[scenario]); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := fileDigest(filepath.Join(source, "runtime.db"))
			if _, err := convert(ctx, source, backup, target); err == nil {
				t.Fatal("accepted", scenario)
			}
			if after, _ := fileDigest(filepath.Join(source, "runtime.db")); after != before {
				t.Fatal("failed conversion modified source")
			}
			if _, err := os.Stat(filepath.Join(target, "conversion.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed conversion left a success receipt")
			}
		})
	}
}

func TestOfflineConversionCopiesUncheckpointedWAL(t *testing.T) {
	source, backup, target := fixture(t)
	db := openFixtureDB(t, source)
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE provider_accounts SET note='durable WAL' WHERE account_id='oauth'`); err != nil {
		t.Fatal(err)
	}
	if _, err := convert(context.Background(), source, backup, target); err != nil {
		t.Fatal(err)
	}
	for _, directory := range []string{backup, target} {
		copied := openFixtureDB(t, directory)
		var note string
		if err := copied.QueryRow(`SELECT note FROM provider_accounts WHERE account_id='oauth'`).Scan(&note); err != nil || note != "durable WAL" {
			t.Fatal("lost WAL", err)
		}
		copied.Close()
	}
}

func TestOfflineConversionRetiresOnlyVerifiedEmptyAccountSelections(t *testing.T) {
	for _, scenario := range []string{"empty_row", "empty_table", "non_empty", "hash", "revision", "extra_column", "missing_metadata", "missing_selections", "orphan"} {
		t.Run(scenario, func(t *testing.T) {
			source, backup, target := fixture(t)
			db := openFixtureDB(t, source)
			for _, definition := range retiredSelectionTables {
				if _, err := db.Exec(definition); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`
			 INSERT INTO capture_account_selection_schema_metadata VALUES(1,1,'17798ddd22330b6ed9d43ae21d86b73929b39e02c06b1c059e652ea1c4e93edb');
			 INSERT INTO capture_environment_assignments VALUES('managed_run','run','limited',1,zeroblob(32),1,'launch','limited',1,zeroblob(32),'[]','[]',zeroblob(32),1000,'','');
			 INSERT INTO capture_account_selections VALUES('managed_run','run','[]');
			 `); err != nil {
				t.Fatal(err)
			}
			changes := map[string]string{
				"empty_row":          `SELECT 1`,
				"empty_table":        `DELETE FROM capture_account_selections`,
				"non_empty":          `UPDATE capture_account_selections SET selections_json='[{"accountId":"oauth"}]'`,
				"hash":               `UPDATE capture_account_selection_schema_metadata SET source_sha256=printf('%064d',0)`,
				"revision":           `PRAGMA ignore_check_constraints=ON; UPDATE capture_account_selection_schema_metadata SET revision=2`,
				"extra_column":       `ALTER TABLE capture_account_selections ADD COLUMN extra TEXT`,
				"missing_metadata":   `DROP TABLE capture_account_selection_schema_metadata`,
				"missing_selections": `DROP TABLE capture_account_selections`,
				"orphan":             `UPDATE capture_account_selections SET capture_id='missing'`,
			}
			if _, err := db.Exec(changes[scenario]); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before, _ := fileDigest(filepath.Join(source, "runtime.db"))
			result, err := convert(context.Background(), source, backup, target)
			for _, directory := range []string{source, backup} {
				if after, digestErr := fileDigest(filepath.Join(directory, "runtime.db")); digestErr != nil || after != before {
					t.Fatal("original selection evidence changed", digestErr)
				}
			}
			if scenario != "empty_row" && scenario != "empty_table" {
				if err == nil {
					t.Fatal("retired unsupported selection data", scenario)
				}
				if _, err := os.Stat(filepath.Join(target, "conversion.json")); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("failed retirement left a success receipt")
				}
				return
			}
			wantRows := int64(1)
			if scenario == "empty_table" {
				wantRows = 0
			}
			if err != nil || len(result.RetiredRows) != 2 || result.RetiredRows["capture_account_selections"] != wantRows || result.RetiredRows["capture_account_selection_schema_metadata"] != 1 {
				t.Fatal("missing explicit retirement receipt", result.RetiredRows, err)
			}
			db = openFixtureDB(t, target)
			defer db.Close()
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name LIKE 'capture_account_selection%'`).Scan(&count); err != nil || count != 0 {
				t.Fatal("retired extension leaked into current schema", count, err)
			}
		})
	}
}

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	// Resolve macOS /var -> /private/var: conversion intentionally rejects aliases.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	db := openFixtureDB(t, source)
	if _, err := db.Exec(releasedSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
	 INSERT INTO provider_accounts(account_id,display_name,credential_origin,endpoint_associations,association_revision,note,note_revision,realm_id,driver_ref,secret_reference,state,revision,created_at_unix_ms,updated_at_unix_ms)
	 VALUES('oauth','OAuth','https://chatgpt.com','[]',1,'keep note',1,'openai.chatgpt','codex_oauth','secret://provider-account/oauth','active',3,1000,2000),
	 ('api','API','https://api.anthropic.com','[]',1,'keep note',1,'anthropic.official','anthropic_api_key','secret://provider-account/api','disabled',2,1000,2000);
	 INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms)
	 VALUES('run',zeroblob(32),randomblob(32),'/workspace','/bin/test','test',1,'finished',1000,3000,2000);
	 INSERT INTO capture_run_projects VALUES('run',json_object('repositoryKey',printf('%064d',0),'repositoryName','renamed-local-clone','branch','main','detached',json('false')));
	 INSERT INTO runtime_users VALUES('member','member',printf('%064d',0),'disabled',1000,1000);
	 INSERT INTO runtime_user_policies VALUES('member',CAST('["limited"]' AS BLOB),42,1000);
	 INSERT INTO acp_observations VALUES('run','{"mode":"off","retentionDays":0}',3000,NULL,1,1);
	 INSERT INTO runtime_egress_attempts(attempt_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,started_at_unix_ms)
	 VALUES('attempt','provider_attempt','client_semantic','upstream_attempt','upstream','known','core','https://provider.example:443','policy',1,'environment','rule','direct',1000);
	 UPDATE sqlite_sequence SET seq=700 WHERE name='runtime_egress_attempts';
	 UPDATE runtime_usage_policy SET enabled=1,revision=4,collecting_since_unix_ms=1000;
	 `); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(2000).UTC()
	observation := runtimeusage.Observation{ExchangeID: "known", CaptureRunID: "run", Source: "local", StartedAt: now, OccurredAt: now,
		Status: "succeeded", EnvironmentID: "limited", EnvironmentRevision: 1, EnvironmentName: "Limited"}
	observation.Usage.InputUncached.Known, observation.Usage.InputUncached.Tokens = true, 123
	observation.Usage.InputUncached.Source = "provider"
	if err := observation.Validate(); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(observation)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	raw["attribution"] = map[string]any{"callerId": "", "callerLabel": "", "callerKind": "", "projectId": "git:" + strings.Repeat("b", 64),
		"gitAtLaunch": map[string]any{"repositoryKey": strings.Repeat("0", 64), "repositoryName": "renamed-local-clone", "branch": "main", "detached": false}}
	data, _ = json.Marshal(raw)
	if _, err := db.Exec(`INSERT INTO runtime_usage_observations VALUES('known','run','','',2000,5000,?)`, string(data)); err != nil {
		t.Fatal(err)
	}
	observation.ExchangeID, observation.Status = "unknown", "failed"
	observation.Usage.InputUncached.Known, observation.Usage.InputUncached.Tokens = false, 0
	observation.Usage.InputUncached.Source = ""
	data, _ = json.Marshal(observation)
	if _, err := db.Exec(`INSERT INTO runtime_usage_observations VALUES('unknown','run','','',2000,5000,?)`, string(data)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"server-secrets/fixture", "local-ca/root-key.pem", "runtime-config.json"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(source, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, name), []byte("synthetic-sensitive-fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return source, filepath.Join(root, "backup"), filepath.Join(root, "converted")
}

func openFixtureDB(t *testing.T, directory string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(directory, "runtime.db")}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	return db
}
