package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

func nativeSQL(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func nativeExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("SQL fixture: %v\n%s", err, query)
	}
}
func nativeSchemaDB(t *testing.T, migrated bool) *sql.DB {
	t.Helper()
	db := openSQLiteForTest(t, filepath.Join(t.TempDir(), "native.db"))
	nativeExec(t, db, "PRAGMA foreign_keys=ON")
	if migrated {
		nativeExec(t, db, schemaV1SQL)
		nativeExec(t, db, nativeSQL(t, "migrations/001_to_002.sql"))
	} else {
		nativeExec(t, db, nativeSQL(t, "schema/v2.sql"))
	}
	return db
}

// Names and columns come only from the trusted frozen schema, never from input.
// Encoding retains SQLite storage classes, including NULL versus an empty blob.
func nativeTypedRows(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var encoded []string
	for rows.Next() {
		values := make([]any, len(cols))
		dest := make([]any, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		var row strings.Builder
		for _, v := range values {
			var s string
			switch x := v.(type) {
			case nil:
				s = "null"
			case int64:
				s = fmt.Sprintf("int:%d", x)
			case float64:
				s = fmt.Sprintf("float:%016x", math.Float64bits(x))
			case string:
				s = "text:" + hex.EncodeToString([]byte(x))
			case []byte:
				s = "blob:" + hex.EncodeToString(x)
			default:
				t.Fatalf("unexpected SQLite type %T", v)
			}
			fmt.Fprintf(&row, "%d:%s;", len(s), s)
		}
		encoded = append(encoded, row.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(encoded)
	return encoded
}
func nativeColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info("` + table + `")`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, `"`+name+`"`)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}
func nativeLegacyColumns(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	result := map[string][]string{}
	for _, table := range tables {
		result[table] = nativeColumns(t, db, table)
	}
	return result
}
func nativeFingerprint(t *testing.T, db *sql.DB, columns map[string][]string) map[string][]string {
	t.Helper()
	result := map[string][]string{}
	for table, cols := range columns {
		result[table] = nativeTypedRows(t, db, `SELECT `+strings.Join(cols, ",")+` FROM "`+table+`"`)
	}
	return result
}

func TestNativeSchemaBootstrapMatchesMigration(t *testing.T) {
	fresh, migrated := nativeSchemaDB(t, false), nativeSchemaDB(t, true)
	a, err := schemaObjects(context.Background(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	b, err := schemaObjects(context.Background(), migrated)
	if err != nil {
		t.Fatal(err)
	}
	for i := range a {
		a[i].statement = strings.Join(strings.Fields(strings.ReplaceAll(a[i].statement, `"`, "")), " ")
	}
	for i := range b {
		b[i].statement = strings.Join(strings.Fields(strings.ReplaceAll(b[i].statement, `"`, "")), " ")
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("v2 bootstrap and delta sqlite_schema differ")
	}
	tables := nativeLegacyColumns(t, fresh)
	count := 0
	for table := range tables {
		if strings.HasPrefix(table, "native_tool_") {
			count++
		}
		for _, pragma := range []string{"table_info", "foreign_key_list", "index_list"} {
			query := `PRAGMA ` + pragma + `("` + table + `")`
			if !reflect.DeepEqual(nativeTypedRows(t, fresh, query), nativeTypedRows(t, migrated, query)) {
				t.Fatalf("%s %s parity", table, pragma)
			}
		}
	}
	if count != 9 {
		t.Fatalf("native table count %d", count)
	}
	nativeOnly := openSQLiteForTest(t, filepath.Join(t.TempDir(), "native-only.db"))
	nativeExec(t, nativeOnly, schemaV1SQL)
	nativeExec(t, nativeOnly, nativeSQL(t, "native_schema.sql"))
	for table := range tables {
		if strings.HasPrefix(table, "native_tool_") {
			if !reflect.DeepEqual(nativeTypedRows(t, fresh, `PRAGMA table_info("`+table+`")`), nativeTypedRows(t, nativeOnly, `PRAGMA table_info("`+table+`")`)) {
				t.Fatal("standalone native DDL differs")
			}
		}
	}
}

func TestNativeAuditRebuildPreservesRowsAndSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	store := openFrozenV1Store(t, path)
	ctx := context.Background()
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	writeAnthropicEndpoint(t, store)
	ref, _ := secretstore.ParseReference("secret://provider-account/native-legacy")
	account := provideraccount.Account{ID: "native-legacy", DisplayName: "Legacy", Origin: providerTestOrigin(t), AssociationRevision: 1, RealmID: "anthropic.official", Driver: providerauth.AnthropicAPIKeyDriverRef(), SecretRef: ref, State: provideraccount.StateActive, Revision: 1, SettingsRevision: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := store.ProviderAccountRepository().Write(ctx, 0, account); err != nil {
		t.Fatal(err)
	}
	manager := newEnvironmentManager(t, store)
	candidate := environmentFixture(t, "native-legacy", 1)
	draft, err := manager.SaveDraft(ctx, environment.DraftCommand{Candidate: candidate})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(ctx, candidate.ID, draft.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Publish(ctx, preview); err != nil {
		t.Fatal(err)
	}
	if err := store.ExchangeContentRepository().Put(ctx, contentRecordFixture(t, "native-legacy-history", now)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EgressAttemptRepository().Append(ctx, providerAttempt(t, "native-legacy-audit")); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, store.database, `INSERT INTO code_library_collections VALUES ('legacy-config','Legacy config')`)
	nativeExec(t, store.database, `INSERT INTO tool_approvals(approval_id,revision,kind,subject_refs_json,subject_labels_json,aggregate_key,state,created_at_unix_ms,expires_at_unix_ms) VALUES ('legacy-approval',1,'client_root_ask',X'5b2261225d',X'5b2262225d','legacy-root','pending',1,2)`)
	nativeExec(t, store.database, `UPDATE runtime_egress_attempts SET sequence=9999,bytes_in=23,bytes_out=17,completed_at_unix_ms=12,outcome='completed',error_class='legacy'`)
	// A deleted high autoincrement row must not permit sequence reuse after rebuild.
	nativeExec(t, store.database, `INSERT INTO runtime_egress_attempts(sequence,attempt_id,purpose,payload_class,parent_kind,parent_id,caller_kind,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,started_at_unix_ms) VALUES (90000,'deleted-high','update','none','runtime_action','legacy','core','https://legacy.example','legacy',1,'runtime','legacy','direct',0,'',0,1)`)
	nativeExec(t, store.database, `DELETE FROM runtime_egress_attempts WHERE sequence=90000`)
	shutdownTestStore(t, store)
	db := openSQLiteForTest(t, path)
	nativeExec(t, db, "PRAGMA foreign_keys=ON")
	frozen := openSQLiteForTest(t, filepath.Join(t.TempDir(), "frozen-v1.db"))
	nativeExec(t, frozen, schemaV1SQL)
	columns := nativeLegacyColumns(t, frozen)
	before := nativeFingerprint(t, db, columns)
	nativeExec(t, db, nativeSQL(t, "migrations/001_to_002.sql"))
	after := nativeFingerprint(t, db, columns)
	if !reflect.DeepEqual(before, after) {
		for table, want := range before {
			if !reflect.DeepEqual(want, after[table]) {
				t.Errorf("legacy columns changed in %s", table)
			}
		}
	}
	var known, sequence int
	if err := db.QueryRow("SELECT byte_counts_known FROM runtime_egress_attempts").Scan(&known); err != nil || known != 1 {
		t.Fatal("legacy byte count default lost")
	}
	if err := db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name='runtime_egress_attempts'").Scan(&sequence); err != nil || sequence != 90000 {
		t.Fatal("deleted high sequence lost")
	}
	for table := range nativeLegacyColumns(t, db) {
		if strings.HasPrefix(table, "native_tool_") {
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM "` + table + `"`).Scan(&count); err != nil || count != 0 {
				t.Fatal("migration invented native rows")
			}
		}
	}
	nativeExec(t, db, `INSERT INTO runtime_egress_attempts(attempt_id,purpose,payload_class,parent_kind,parent_id,caller_kind,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,started_at_unix_ms) VALUES ('after','update','none','runtime_action','legacy','core','https://legacy.example','legacy',1,'runtime','legacy','direct',0,'',0,1)`)
	if err := db.QueryRow("SELECT sequence FROM runtime_egress_attempts WHERE attempt_id='after'").Scan(&sequence); err != nil || sequence != 90001 {
		t.Fatal("audit sequence reused")
	}
	if err := db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&sequence); err != nil || sequence != 0 {
		t.Fatal("legacy FK loss")
	}
}

func TestNativeAuditRebuildPreservesAbsentSequence(t *testing.T) {
	db := openSQLiteForTest(t, filepath.Join(t.TempDir(), "empty-v1.db"))
	nativeExec(t, db, schemaV1SQL)
	columns := nativeLegacyColumns(t, db)
	before := nativeFingerprint(t, db, columns)
	nativeExec(t, db, nativeSQL(t, "migrations/001_to_002.sql"))
	after := nativeFingerprint(t, db, columns)
	if !reflect.DeepEqual(before, after) {
		for table, want := range before {
			if !reflect.DeepEqual(want, after[table]) {
				t.Errorf("empty migration changed %s: before=%v after=%v", table, want, after[table])
			}
		}
	}
}

func TestNativeSchemaConstraints(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		t.Run(fmt.Sprint(migrated), func(t *testing.T) {
			db := nativeSchemaDB(t, migrated)
			nativeExec(t, db, `INSERT INTO environment_revision_counters VALUES ('native',1,0)`)
			nativeExec(t, db, `INSERT INTO environment_revisions VALUES ('native',1,'Native','active',1,X'7b7d',zeroblob(32),1)`)
			nativeExec(t, db, `INSERT INTO native_tool_scopes VALUES ('managed_run','capture',1,1,'active',0,100)`)
			nativeExec(t, db, `INSERT INTO native_tool_sessions VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','managed_run','capture','native',1,zeroblob(32),1,'native.mcp',1,'https://chatgpt.com','2025-06-18',1,1,'ready',1,zeroblob(32),100)`)
			nativeExec(t, db, `INSERT INTO native_tool_operations VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','managed_run','capture','native',1,zeroblob(32),'connection','https://chatgpt.com','POST','tools_call',1,'received','none','off',0,1,100,NULL,NULL,NULL,'')`)
			transaction, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			bodyDigest, err := storeEvidenceBody(context.Background(), transaction, []byte("synthetic native content"))
			if err != nil {
				t.Fatal(err)
			}
			if err := transaction.Commit(); err != nil {
				t.Fatal(err)
			}
			deny := func(query string) {
				t.Helper()
				if _, err := db.Exec(query); err == nil {
					t.Fatalf("invalid SQL accepted: %s", query)
				}
			}
			deny(`UPDATE native_tool_operations SET rpc_kind='unknown'`)
			deny(`UPDATE native_tool_operations SET state='started'`)
			deny(`INSERT INTO native_tool_bindings VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,0,zeroblob(32),'native.mcp',1,1,1,zeroblob(32),zeroblob(32),zeroblob(32),zeroblob(32),NULL,NULL,NULL,'')`)
			nativeExec(t, db, `INSERT INTO native_tool_bindings VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,1,zeroblob(32),'native.mcp',1,1,1,zeroblob(32),zeroblob(32),zeroblob(32),zeroblob(32),NULL,NULL,NULL,'')`)
			deny(`UPDATE native_tool_bindings SET owner_epoch=2`)
			nativeExec(t, db, `UPDATE native_tool_operations SET state='validated',revision=2`)
			deny(`UPDATE native_tool_operations SET state='completed'`)
			nativeExec(t, db, `UPDATE native_tool_operations SET state='completed',completed_at_unix_ms=3,terminal_reason='completed'`)
			deny(`UPDATE native_tool_operations SET state='received',completed_at_unix_ms=NULL,terminal_reason=''`)
			deny(`INSERT INTO native_tool_approvals VALUES ('cccccccccccccccccccccccccccccccc','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',zeroblob(32),1,'denied',1,10,2,'native-decision-0001',3)`)
			nativeExec(t, db, `INSERT INTO native_tool_approvals VALUES ('cccccccccccccccccccccccccccccccc','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',zeroblob(32),1,'allowed',1,10,2,'native-decision-0001',3)`)
			deny(`UPDATE native_tool_approvals SET state='pending'`)
			deny(`UPDATE native_tool_approvals SET idempotency_key=''`)
			contentInsert := fmt.Sprintf(`INSERT INTO native_tool_contents VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','tool_arguments',X'%x',100)`, bodyDigest)
			deny(contentInsert)
			nativeExec(t, db, `UPDATE native_tool_operations SET recording='metadata_only',retention_days=30`)
			deny(contentInsert)
			nativeExec(t, db, `UPDATE native_tool_operations SET recording='full',retention_days=30,rpc_kind='ping'`)
			deny(contentInsert)
			nativeExec(t, db, `UPDATE native_tool_operations SET rpc_kind='tools_call'`)
			nativeExec(t, db, contentInsert)
			deny(`UPDATE native_tool_contents SET body_digest=zeroblob(32)`)
			deny(`UPDATE native_tool_contents SET part='auth'`)
			deny(`UPDATE native_tool_contents SET operation_id='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'`)
			deny(contentInsert)
			nativeExec(t, db, `UPDATE native_tool_operations SET recording='off',retention_days=0`)
			deny(`UPDATE native_tool_contents SET expires_at_unix_ms=101`)
			nativeExec(t, db, `DELETE FROM native_tool_contents`)
			deny(`INSERT INTO native_tool_scopes VALUES ('managed_run','bad',1,0,'active',0,100)`)
			for _, query := range []string{
				`UPDATE native_tool_scopes SET capture_kind='invented'`, `UPDATE native_tool_scopes SET capture_id=''`, `UPDATE native_tool_scopes SET revision=0`, `UPDATE native_tool_scopes SET owner_epoch=-1`, `UPDATE native_tool_scopes SET state='invented'`, `UPDATE native_tool_scopes SET replay_barrier=2`,
				`UPDATE native_tool_sessions SET session_id='AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'`, `UPDATE native_tool_sessions SET environment_revision=0`, `UPDATE native_tool_sessions SET environment_digest=zeroblob(31)`, `UPDATE native_tool_sessions SET assignment_revision=0`, `UPDATE native_tool_sessions SET route_id=''`, `UPDATE native_tool_sessions SET route_revision=0`, `UPDATE native_tool_sessions SET target_origin=''`, `UPDATE native_tool_sessions SET protocol_version='future'`, `UPDATE native_tool_sessions SET revision=0`, `UPDATE native_tool_sessions SET state='invented'`, `UPDATE native_tool_sessions SET catalog_digest=zeroblob(31)`,
				`UPDATE native_tool_operations SET operation_id='short'`, `UPDATE native_tool_operations SET connection_id=''`, `UPDATE native_tool_operations SET target_origin=''`, `UPDATE native_tool_operations SET method='PUT'`, `UPDATE native_tool_operations SET revision=0`, `UPDATE native_tool_operations SET decision_source='invented'`, `UPDATE native_tool_operations SET recording='invented'`, `UPDATE native_tool_operations SET retention_days=3651`, `UPDATE native_tool_operations SET metadata_expires_at_unix_ms=1`, `UPDATE native_tool_operations SET bytes_in=-1`, `UPDATE native_tool_operations SET bytes_out=-1`, `UPDATE native_tool_operations SET terminal_reason='invented'`,
				`UPDATE native_tool_approvals SET approval_id='short'`, `UPDATE native_tool_approvals SET binding_digest=zeroblob(31)`, `UPDATE native_tool_approvals SET revision=0`, `UPDATE native_tool_approvals SET state='invented'`, `UPDATE native_tool_approvals SET expires_at_unix_ms=1`, `UPDATE native_tool_approvals SET resolved_at_unix_ms=0`, `UPDATE native_tool_approvals SET consumed_at_unix_ms=1`,
			} {
				deny(query)
			}
			deny(`UPDATE native_tool_sessions SET catalog_generation=0`)
			deny(`UPDATE native_tool_sessions SET owner_epoch=0`)
			deny(`UPDATE native_tool_sessions SET capture_id='missing'`)
			deny(`UPDATE native_tool_sessions SET environment_id='missing'`)
			deny(`UPDATE native_tool_operations SET capture_id='missing'`)
			deny(`UPDATE native_tool_operations SET environment_id='missing'`)
			deny(`INSERT INTO native_tool_inbound VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',zeroblob(32),'unclassified',zeroblob(32),'dddddddddddddddddddddddddddddddd')`)
			nativeExec(t, db, `INSERT INTO native_tool_inbound VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',zeroblob(32),'tools_call',zeroblob(32),'dddddddddddddddddddddddddddddddd')`)
			deny(`INSERT INTO native_tool_inbound VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',zeroblob(32),'tools_call',zeroblob(32),'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee')`)
			deny(`INSERT INTO native_tool_inbound VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',randomblob(32),'ping',zeroblob(32),'dddddddddddddddddddddddddddddddd')`)
			deny(`UPDATE native_tool_inbound SET session_id='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'`)
			deny(`UPDATE native_tool_inbound SET rpc_id_digest=zeroblob(31)`)
			deny(`UPDATE native_tool_inbound SET request_digest=zeroblob(31)`)
			nativeExec(t, db, `INSERT INTO native_tool_once VALUES ('managed_run','capture',zeroblob(32),'dddddddddddddddddddddddddddddddd',zeroblob(32),'attempt',1,1)`)
			deny(`UPDATE native_tool_once SET replay_key=zeroblob(31)`)
			deny(`UPDATE native_tool_once SET intent_digest=zeroblob(31)`)
			deny(`UPDATE native_tool_once SET attempt_id=''`)
			deny(`UPDATE native_tool_once SET authority_revision=0`)
			deny(`INSERT INTO native_tool_once VALUES ('managed_run','capture',zeroblob(32),'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',zeroblob(32),'attempt2',1,1)`)
			deny(`INSERT INTO native_tool_once VALUES ('managed_run','capture',randomblob(32),'dddddddddddddddddddddddddddddddd',zeroblob(32),'attempt2',1,1)`)
			deny(`INSERT INTO native_tool_once VALUES ('managed_run','capture',randomblob(32),'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',zeroblob(32),'attempt',1,1)`)
			deny(`UPDATE native_tool_once SET capture_id='missing'`)
			deny(`INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES ('dddddddddddddddddddddddddddddddd',1,'received',1)`)
			nativeExec(t, db, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,'received',1)`)
			deny(`UPDATE native_tool_events SET operation_revision=0`)
			deny(`UPDATE native_tool_events SET kind='invented'`)
			deny(`INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES ('bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',1,'validated',2)`)
			nativeExec(t, db, `INSERT INTO runtime_egress_attempts(attempt_id,purpose,payload_class,parent_kind,parent_id,caller_kind,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,started_at_unix_ms,byte_counts_known) VALUES ('native','client_tool_service','client_data','client_operation','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','core','https://chatgpt.com','native',1,'environment','native.mcp','direct',0,'',0,1,0)`)
			for _, query := range []string{`UPDATE runtime_egress_attempts SET byte_counts_known=2`, `UPDATE runtime_egress_attempts SET account_id='managed',account_settings_revision=1,proxy_revision=1`, `UPDATE runtime_egress_attempts SET parent_exchange_id='exchange'`, `UPDATE runtime_egress_attempts SET payload_class='client_semantic'`, `UPDATE runtime_egress_attempts SET caller_kind='plugin'`, `UPDATE runtime_egress_attempts SET policy_authority='runtime'`, `UPDATE runtime_egress_attempts SET parent_kind='upstream_attempt'`} {
				deny(query)
			}
			// Missing operation FKs on inbound/once are deliberate safety tombstones;
			// deleting metadata must retain those identities, while dependent rows cascade.
			nativeExec(t, db, `DELETE FROM native_tool_operations`)
			var count int
			for _, table := range []string{"native_tool_bindings", "native_tool_approvals", "native_tool_events"} {
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("metadata cascade failed")
				}
			}
			for _, table := range []string{"native_tool_inbound", "native_tool_once"} {
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
					t.Fatal("safety tombstone erased")
				}
			}
			nativeExec(t, db, `DELETE FROM native_tool_scopes`)
			for _, table := range []string{"native_tool_sessions", "native_tool_inbound", "native_tool_once"} {
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatal("retired scope cascade failed")
				}
			}
		})
	}
}
