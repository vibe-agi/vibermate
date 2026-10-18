package runtimepersistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// This fixture is already-used released v2, including data that the v1-only
// proof must reject. Both actual released construction paths are exercised.
func populatedV2MigrationFixture(t *testing.T, fresh bool) migrationFixture {
	t.Helper()
	f := populatedV1MigrationFixture(t)
	for _, dir := range []string{f.source, f.snapshot} {
		db := openSQLiteForTest(t, filepath.Join(dir, "runtime.db"))
		nativeExec(t, db, migration001To002SQL)
		nativeExec(t, db, `UPDATE runtime_metadata SET schema_revision=2;
UPDATE runtime_egress_default SET revision=7,egress_id='profile.example',egress_revision=1;
UPDATE provider_accounts SET settings_revision=8,note='keep note',note_revision=9,automatic_refresh=0;
UPDATE runtime_connection_events SET inspection_purpose='client_tools';
UPDATE runtime_egress_attempts SET byte_counts_known=0,selection_source='global',default_revision=7;
INSERT INTO native_tool_scopes VALUES('managed_run','capture',1,1,'active',0,100);
INSERT INTO native_tool_sessions VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','managed_run','capture','native-legacy',1,zeroblob(32),1,'native.mcp',1,'https://chatgpt.com','2025-06-18',1,1,'ready',1,zeroblob(32),100);
INSERT INTO native_tool_operations VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','managed_run','capture','native-legacy',1,zeroblob(32),'connection','https://chatgpt.com','POST','tools_call',1,'received','none','off',0,1,100,NULL,NULL,NULL,'');
INSERT INTO native_tool_bindings VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,1,zeroblob(32),'native.mcp',1,1,1,zeroblob(32),zeroblob(32),zeroblob(32),zeroblob(32),NULL,NULL,NULL,'',NULL);
INSERT INTO native_tool_approvals VALUES('cccccccccccccccccccccccccccccccc','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',zeroblob(32),1,'allowed',1,10,2,'native-decision-0001',3);
INSERT INTO native_tool_once VALUES('managed_run','capture',zeroblob(32),'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',zeroblob(32),'native-attempt',3,1);
INSERT INTO runtime_users VALUES('v2-user','v2-user',printf('%064d',0),'disabled',1,9);
INSERT INTO runtime_user_login_sessions VALUES('v2-login','v2-user',zeroblob(32),'v2-machine','v2-device',1,100,9);
INSERT INTO runtime_user_policies VALUES('v2-user',0,X'5b226e61746976652d6c6567616379225d',17,23);
INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms,runtime_user_id,runtime_username,login_session_id,device_name) VALUES('v2-capture',zeroblob(32),zeroblob(32),'/synthetic','/synthetic/codex','codex',1,'finished',1,100,9,'v2-user','v2-user','v2-login','v2-device');
INSERT INTO acp_observations VALUES('v2-capture','full',100,X'000102ff',7,1);
INSERT INTO native_tool_inbound VALUES('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',zeroblob(32),'tools_call',zeroblob(32),'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb');
UPDATE native_tool_operations SET recording='full',retention_days=30,state='completed',revision=2,completed_at_unix_ms=3,terminal_reason='completed',bytes_in=NULL,bytes_out=NULL;
INSERT INTO native_tool_contents SELECT 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','tool_arguments',digest,100 FROM runtime_evidence_bodies ORDER BY digest LIMIT 1;
INSERT INTO native_tool_events VALUES(90000,'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',3,'progress',3);
DELETE FROM native_tool_events WHERE sequence=90000;
INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',2,'completed',3);
`)
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if fresh {
			rehouseFrozenV2(t, filepath.Join(dir, "runtime.db"))
		}
	}
	db := openSQLiteForTest(t, filepath.Join(f.source, "runtime.db"))
	f.columns = nativeLegacyColumns(t, db)
	f.before = nativeFingerprint(t, db, f.columns)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func rehouseFrozenV2(t *testing.T, path string) {
	t.Helper()
	dst := path + ".fresh"
	db := openSQLiteForTest(t, dst)
	nativeExec(t, db, "PRAGMA foreign_keys=OFF;")
	nativeExec(t, db, schemaV2SQL)
	if _, err := db.Exec(`ATTACH DATABASE ? AS old`, path); err != nil {
		t.Fatal(err)
	}
	objects, err := schemaObjects(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		if o.kind == "trigger" {
			nativeExec(t, db, `DROP TRIGGER `+quoteMigrationIdentifier(o.name))
		}
	}
	cols := nativeLegacyColumns(t, db)
	tables := make([]string, 0, len(cols))
	for table := range cols {
		if table != "sqlite_sequence" {
			tables = append(tables, table)
		}
	}
	sort.Strings(tables)
	tables = append(tables, "sqlite_sequence")
	for _, table := range tables {
		columns := cols[table]
		quoted := make([]string, len(columns))
		for i, c := range columns {
			quoted[i] = c
		}
		// nativeLegacyColumns returns unquoted column names.
		query := `DELETE FROM main.` + quoteMigrationIdentifier(table) + `; INSERT INTO main.` + quoteMigrationIdentifier(table) + ` SELECT ` + joinMigrationColumns(quoted) + ` FROM old.` + quoteMigrationIdentifier(table)
		nativeExec(t, db, query)
	}
	for _, o := range objects {
		if o.kind == "trigger" {
			nativeExec(t, db, o.statement)
		}
	}
	nativeExec(t, db, `DETACH DATABASE old`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dst, path); err != nil {
		t.Fatal(err)
	}
}
func joinMigrationColumns(columns []string) string {
	var s string
	for i, c := range columns {
		if i > 0 {
			s += ","
		}
		s += c
	}
	return s
}

func TestRevision2MigrationPreservesAllInstalledValues(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "alter", true: "bootstrap"}[fresh], func(t *testing.T) {
			f := populatedV2MigrationFixture(t, fresh)
			verified, err := VerifyRevision2MigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			result, err := UpgradeRevision2(context.Background(), Revision2MigrationInput{Database: verified, ReconcileTimeout: time.Second})
			if err != nil || !result.Committed || result.FromRevision != 2 || result.ToRevision != 3 {
				t.Fatalf("upgrade=%+v %v", result, err)
			}
			db := openSQLiteForTest(t, filepath.Join(f.source, "runtime.db"))
			after := nativeFingerprint(t, db, f.columns)
			delete(after, "runtime_metadata")
			delete(f.before, "runtime_metadata")
			if !reflect.DeepEqual(after, f.before) {
				t.Fatal("v2 fields or sequences changed")
			}
			var mode string
			var revision int
			if err := db.QueryRow(`SELECT cyber_access_program,settings_revision FROM provider_accounts`).Scan(&mode, &revision); err != nil || mode != "standard" || revision != 8 {
				t.Fatalf("mode=%q settings=%d %v", mode, revision, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			store := openTestStore(t, filepath.Join(f.source, "runtime.db"))
			closeFrozenV1Store(t, store)
		})
	}
}

func TestRevision2MigrationRejectsUnverifiedAndChangedSources(t *testing.T) {
	if _, err := UpgradeRevision2(context.Background(), Revision2MigrationInput{}); !errors.Is(err, ErrMigrationNotCommitted) {
		t.Fatal(err)
	}
	f := populatedV2MigrationFixture(t, false)
	v, err := VerifyRevision2MigrationDatabase(context.Background(), filepath.Join(f.source, "runtime.db"), filepath.Join(f.snapshot, "runtime.db"), f.owner, f.snapshotOwner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	db := openSQLiteForTest(t, filepath.Join(f.source, "runtime.db"))
	nativeExec(t, db, `UPDATE native_tool_scopes SET replay_barrier=1`)
	db.Close()
	if _, err := UpgradeRevision2(context.Background(), Revision2MigrationInput{Database: v, ReconcileTimeout: time.Second}); !errors.Is(err, ErrMigrationOutcomeUnknown) {
		t.Fatalf("changed full-v2 source: %v", err)
	}
}

func TestRevision2MigrationCertifiesStandardOnlyBeforeAdmission(t *testing.T) {
	f := populatedV2MigrationFixture(t, false)
	runner := revision2MigrationRunner{afterDDL: func(ctx context.Context, conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `UPDATE provider_accounts SET cyber_access_program='daybreak_red'`)
		return err
	}}
	result, err := runner.upgrade(context.Background(), f.input2(t))
	if result.Committed || !errors.Is(err, ErrMigrationNotCommitted) {
		t.Fatalf("invented opt-in committed %+v %v", result, err)
	}
	f.assertLegacyPreserved(t)
	if _, err := UpgradeRevision2(context.Background(), f.input2(t)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.source, "runtime.db")
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, `UPDATE provider_accounts SET credential_origin='https://chatgpt.com',realm_id='openai.chatgpt',driver_ref='static_header',cyber_access_program='daybreak_red'`)
	db.Close()
	store := openTestStore(t, path)
	defer closeFrozenV1Store(t, store)
	a, exists, err := store.ProviderAccountRepository().Load(context.Background(), "native-legacy")
	if err != nil || !exists || a.CyberAccessProgram.WireValue() != "daybreak_red" {
		t.Fatalf("normal3 rejected real mode %+v %v", a, err)
	}
}
