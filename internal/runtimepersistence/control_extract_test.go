package runtimepersistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func extractionFixture(t *testing.T, revision int) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	ddl := schemaV1SQL
	if revision == 2 {
		ddl = schemaV2SQL
	}
	nativeExec(t, db, ddl)
	if _, err := db.Exec(`INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',?,'2026-10-03T00:00:00Z')`, revision); err != nil {
		t.Fatal(err)
	}
	nativeExec(t, db, `UPDATE runtime_usage_policy SET enabled=0,retention_days=7,revision=19,collecting_since_unix_ms=NULL; INSERT INTO runtime_usage_retention_caps(retention_days,after_sequence,through_sequence) VALUES(3,11,71); INSERT INTO code_library_collections VALUES('authored','Preserved bytes')`)
	return path, db
}

func TestControlExtractionReleasedSchemasPreserveConsentAndIdentity(t *testing.T) {
	for _, revision := range []int{1, 2} {
		t.Run(string(rune('0'+revision)), func(t *testing.T) {
			source, _ := extractionFixture(t, revision)
			before, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "control.db")
			proof, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout-original", ArchiveID: "archive-original"})
			if err != nil {
				t.Fatal(err)
			}
			if proof.Source.Revision != int64(revision) || proof.Source.InitializedAt != "2026-10-03T00:00:00Z" {
				t.Fatalf("wrong provenance: %+v", proof.Source)
			}
			after, _ := os.ReadFile(source)
			if string(before) != string(after) {
				t.Fatal("source changed")
			}
			db, err := sql.Open("sqlite", target)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var identity, initialized, archive, label string
			var enabled, retention, policy, lower, upper int64
			if err := db.QueryRow(`SELECT schema_identity,initialized_at FROM runtime_metadata`).Scan(&identity, &initialized); err != nil {
				t.Fatal(err)
			}
			if identity != "vibermate-control" || initialized != "2026-10-03T00:00:00Z" {
				t.Fatal("installation metadata replaced")
			}
			if err := db.QueryRow(`SELECT enabled,retention_days,revision FROM runtime_usage_policy WHERE collecting_since_unix_ms IS NULL`).Scan(&enabled, &retention, &policy); err != nil {
				t.Fatal(err)
			}
			if enabled != 0 || retention != 7 || policy != 19 {
				t.Fatal("consent replaced")
			}
			if err := db.QueryRow(`SELECT archive_id,after_sequence,through_sequence FROM runtime_usage_legacy_retention_caps WHERE retention_days=3`).Scan(&archive, &lower, &upper); err != nil {
				t.Fatal(err)
			}
			if archive != "archive-original" || lower != 11 || upper != 71 {
				t.Fatal("legacy obligation changed")
			}
			if err := db.QueryRow(`SELECT display_name FROM code_library_collections WHERE collection_id='authored'`).Scan(&label); err != nil {
				t.Fatal(err)
			}
			if label != "Preserved bytes" {
				t.Fatal("authored data changed")
			}
		})
	}
}

func TestControlExtractionOptionalChecksDoNotAuthorizeRequiredDamage(t *testing.T) {
	for _, required := range []bool{false, true} {
		name := "optional-check"
		if required {
			name = "required-check"
		}
		t.Run(name, func(t *testing.T) {
			source, db := extractionFixture(t, 2)
			nativeExec(t, db, `PRAGMA ignore_check_constraints=ON`)
			if required {
				nativeExec(t, db, `UPDATE runtime_usage_retention_caps SET after_sequence=through_sequence`)
			} else {
				nativeExec(t, db, `INSERT INTO runtime_raw_evidence_redaction VALUES(1,X'01')`)
			}
			nativeExec(t, db, `PRAGMA ignore_check_constraints=OFF`)
			_, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "layout", ArchiveID: "archive"})
			if required && err == nil {
				t.Fatal("damaged consent admitted")
			}
			if !required && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestControlExtractionMixedCaptureAndACP(t *testing.T) {
	for _, damage := range []string{"optional", "policy", "expiry-type", "capture-required"} {
		t.Run(damage, func(t *testing.T) {
			source, db := extractionFixture(t, 2)
			nativeExec(t, db, `INSERT INTO capture_runs(run_id,proxy_capability_hash,control_capability_hash,cwd,canonical_executable_path,executable_label,client_catalog_revision,state,created_at_unix_ms,expires_at_unix_ms,updated_at_unix_ms) VALUES('run',randomblob(32),randomblob(32),'/tmp','/bin/tool','tool',1,'finished',10,20,20);
INSERT INTO manual_captures VALUES('manual','local_installation','','Manual','cli','temporary','expired',1,randomblob(32),'waiting_for_traffic',10,20,20,NULL);
INSERT INTO acp_observations(run_id,policy,revision,snapshot,final,expires_at_unix_ms) VALUES('run','{"mode":"full","retentionDays":7}',1,X'01',0,30);
PRAGMA ignore_check_constraints=ON;
UPDATE capture_runs SET observation='broken',first_observed_at_unix_ms=-100;
UPDATE manual_captures SET observation='broken',last_observed_at_unix_ms=-100;
UPDATE acp_observations SET revision=-1,final=9,snapshot=X'ff';`)
			switch damage {
			case "policy":
				nativeExec(t, db, `UPDATE acp_observations SET policy='{"mode":"off","retentionDays":7}'`)
			case "expiry-type":
				nativeExec(t, db, `UPDATE acp_observations SET expires_at_unix_ms='not-an-integer'`)
			case "capture-required":
				nativeExec(t, db, `UPDATE manual_captures SET updated_at_unix_ms=0`)
			}
			nativeExec(t, db, `PRAGMA ignore_check_constraints=OFF`)
			target := filepath.Join(t.TempDir(), "control.db")
			_, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout", ArchiveID: "archive"})
			if damage != "optional" {
				if err == nil {
					t.Fatal("required marker/capture damage admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := sql.Open("sqlite", target)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			var count int
			if err = out.QueryRow(`SELECT count(*) FROM acp_registrations WHERE run_id='run' AND recording='full' AND retention_days=7 AND expires_at_unix_ms=30 AND archive_epoch=1 AND capture_generation=1`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("marker lost: %d %v", count, err)
			}
			if err = out.QueryRow(`SELECT count(*) FROM capture_runs WHERE observation='waiting_for_traffic' AND first_observed_at_unix_ms IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatal("managed display not normalized")
			}
			if err = out.QueryRow(`SELECT count(*) FROM manual_captures WHERE observation='waiting_for_traffic' AND last_observed_at_unix_ms IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatal("manual display not normalized")
			}
		})
	}
}

func TestControlExtractionNativeFinalTupleAndRetiredBarrier(t *testing.T) {
	source, db := extractionFixture(t, 2)
	candidate := environmentFixture(t, "legacy-env", 1)
	candidate.ContentRecording = environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}
	seedFrozenV1Environment(t, &Store{database: db}, candidate, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	digest, err := environment.Digest(candidate)
	if err != nil {
		t.Fatal(err)
	}
	nativeExec(t, db, `INSERT INTO native_tool_scopes VALUES('manual_capture','capture',1,1,'active',0,10000)`)
	_, err = db.Exec(`INSERT INTO native_tool_operations(operation_id,capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,rpc_kind,revision,state,decision_source,recording,retention_days,created_at_unix_ms,metadata_expires_at_unix_ms,terminal_reason) VALUES('11111111111111111111111111111111','manual_capture','capture','legacy-env',1,?,'connection','https://example.com','POST','unclassified',1,'received','none','off',0,10,10000,'')`, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	nativeExec(t, db, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES('11111111111111111111111111111111',1,'received',8000);
UPDATE native_tool_operations SET state='canceled',revision=2,completed_at_unix_ms=4000,terminal_reason='revoked' WHERE operation_id='11111111111111111111111111111111';
INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) VALUES('11111111111111111111111111111111',2,'revoked',4000);
INSERT INTO native_tool_once VALUES('manual_capture','capture',zeroblob(32),'22222222222222222222222222222222',zeroblob(32),'retired-attempt',100,7)`)
	target := filepath.Join(t.TempDir(), "control.db")
	_, err = ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout", ArchiveID: "archive"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var state, event string
	var at, maximum, revision int64
	if err = out.QueryRow(`SELECT state,revision,last_event_kind,last_event_at_unix_ms,maximum_event_at_unix_ms FROM native_tool_operations`).Scan(&state, &revision, &event, &at, &maximum); err != nil {
		t.Fatal(err)
	}
	if state != "canceled" || revision != 2 || event != "revoked" || at != 4000 || maximum != 8000 {
		t.Fatal("native final tuple changed")
	}
	var count int
	if err = out.QueryRow(`SELECT count(*) FROM native_tool_once WHERE operation_id='22222222222222222222222222222222' AND attempt_id='retired-attempt' AND authority_revision=7`).Scan(&count); err != nil || count != 1 {
		t.Fatal("retired barrier lost")
	}
	if _, err = out.Exec(`INSERT INTO native_tool_operations SELECT '33333333333333333333333333333333',capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,rpc_kind,revision,state,decision_source,recording,retention_days,created_at_unix_ms,recording_archive_epoch,recording_capture_generation,last_event_kind,last_event_at_unix_ms,maximum_event_at_unix_ms,metadata_expires_at_unix_ms,completed_at_unix_ms,bytes_in,bytes_out,terminal_reason FROM native_tool_operations`); err == nil {
		t.Fatal("final lifecycle trigger missing")
	}
}

// Build a released-v2 on-disk fixture from real present authority transactions.
// Only tests use schema introspection to construct old rows; production admission
// and extraction use the explicit frozen projections.
func legacyNativeFixture(t *testing.T, store *Store) (string, *sql.DB) {
	t.Helper()
	path, db := extractionFixture(t, 2)
	if _, err := db.Exec(`ATTACH DATABASE ? AS fixture`, store.databasePath); err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for _, name := range []string{"native_tool_operation_received", "native_tool_binding_egress_insert_guard"} {
		var ddl string
		if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, ddl)
		nativeExec(t, db, `DROP TRIGGER `+name)
	}
	tables := append(append([]controlProjection(nil), controlProjections...), nativeControlProjections...)
	for _, p := range tables {
		rows, err := db.Query(`PRAGMA main.table_info(` + p.table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var cid, nn, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &nn, &def, &pk); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, name)
		}
		if err = rows.Close(); err != nil {
			t.Fatal(err)
		}
		nativeExec(t, db, `DELETE FROM main.`+p.table)
		nativeExec(t, db, `INSERT INTO main.`+p.table+` (`+strings.Join(columns, ",")+`) SELECT `+strings.Join(columns, ",")+` FROM fixture.`+p.table)
	}
	nativeExec(t, db, `INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) SELECT operation_id,revision,last_event_kind,last_event_at_unix_ms FROM fixture.native_tool_operations`)
	columns := `sequence,attempt_id,connection_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,caller_id,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,selection_source,default_revision,reused_transport,started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in,byte_counts_known`

	// Rewrite the projection's source only; every provenance constant is explicit.
	nativeExec(t, db, `INSERT INTO runtime_egress_attempts (`+columns+`) `+strings.Replace(strings.Replace(nativeDeliverySelect, `SELECT 1,`, `SELECT row_number() OVER(),`, 1), `FROM native_tool_deliveries`, `FROM fixture.native_tool_deliveries`, 1))
	for _, ddl := range triggers {
		nativeExec(t, db, ddl)
	}
	nativeExec(t, db, `DETACH DATABASE fixture`)
	return path, db
}

func TestControlExtractionNativeDeliveryProof(t *testing.T) {
	for _, damage := range []string{"none", "missing-claim", "missing-delivery", "wrong-provenance"} {
		t.Run(damage, func(t *testing.T) {
			store, id, revision := nativeExecutionHandshakeFixture(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			repo := nativeFullRepository(t, store)
			nativeAllow(t, repo, id, now)
			if _, err := repo.Start(context.Background(), id, "cutover-delivery", revision, now); err != nil {
				t.Fatal(err)
			}
			source, db := legacyNativeFixture(t, store)
			switch damage {
			case "missing-claim":
				nativeExec(t, db, `DELETE FROM native_tool_once`)
			case "missing-delivery":
				nativeExec(t, db, `DELETE FROM runtime_egress_attempts`)
			case "wrong-provenance":
				nativeExec(t, db, `UPDATE runtime_egress_attempts SET connection_id='wrong'`)
			}
			target := filepath.Join(t.TempDir(), "control.db")
			_, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout", ArchiveID: "archive"})
			if damage != "none" {
				if err == nil {
					t.Fatal("unproven delivery admitted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := sql.Open("sqlite", target)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			var count int
			if err = out.QueryRow(`SELECT count(*) FROM native_tool_deliveries WHERE attempt_id='cutover-delivery' AND completed_at_unix_ms IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatal("physical open delivery lost")
			}
		})
	}
}

func TestControlExtractionRequiredSemanticProof(t *testing.T) {
	for _, damage := range []string{"none", "environment-digest", "profile-policy", "missing-consent"} {
		t.Run(damage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.db")
			store := openFrozenV1Store(t, path)
			seedFrozenV1Environment(t, store, environmentFixture(t, "legacy-env", 1), time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
			profile := egressprofile.Direct()
			encoded, marshalErr := json.Marshal(profile.Policy)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			nativeExec(t, store.database, `INSERT INTO egress_profile_heads VALUES('profile.direct',1)`)
			if _, insertErr := store.database.Exec(`INSERT INTO egress_profile_revisions VALUES('profile.direct',1,'Direct',?,1760000000000)`, encoded); insertErr != nil {
				t.Fatal(insertErr)
			}
			switch damage {
			case "environment-digest":
				nativeExec(t, store.database, `UPDATE environment_revisions SET candidate_digest=zeroblob(32)`)
			case "profile-policy":
				nativeExec(t, store.database, `UPDATE egress_profile_revisions SET policy_json=X'7b7d'`)
			case "missing-consent":
				nativeExec(t, store.database, `DELETE FROM runtime_usage_policy`)
			}
			closeFrozenV1Store(t, store)
			_, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: path, TargetPath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "layout", ArchiveID: "archive"})
			if damage == "none" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("required semantic damage admitted")
			}
		})
	}
}

func TestControlExtractionRetiredOpenAuditRemainsHistory(t *testing.T) {
	for _, kind := range []string{"retired-open", "retired-bad-audit", "retained-terminal-open", "extra-unclaimed"} {
		t.Run(kind, func(t *testing.T) {
			store, id, revision := nativeExecutionHandshakeFixture(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			repo := nativeFullRepository(t, store)
			nativeAllow(t, repo, id, now)
			receipt, err := repo.Start(ctx, id, "actual-attempt", revision, now)
			if err != nil {
				t.Fatal(err)
			}
			binding, _ := nativeDecisionFixture(t, store, id)
			stream, err := repo.OpenObservedStream(ctx, receipt, binding, time.Minute, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CompleteStreamRPC(ctx, stream, nativetool.LogicalFinishInput{Outcome: "completed", Reason: "completed", ObservedAt: now}); err != nil {
				t.Fatal(err)
			}
			source, db := legacyNativeFixture(t, store)
			if strings.HasPrefix(kind, "retired-") {
				nativeExec(t, db, `PRAGMA foreign_keys=ON`)
				if _, err = db.Exec(`DELETE FROM native_tool_operations WHERE operation_id=?`, id); err != nil {
					t.Fatal(err)
				}
				if kind == "retired-bad-audit" {
					nativeExec(t, db, `PRAGMA ignore_check_constraints=ON;UPDATE runtime_egress_attempts SET payload_class='unsupported' WHERE attempt_id='actual-attempt';PRAGMA ignore_check_constraints=OFF`)
				}
			}
			if kind == "extra-unclaimed" {
				nativeExec(t, db, `INSERT INTO runtime_egress_attempts(attempt_id,connection_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,caller_id,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,selection_source,default_revision,account_id,account_settings_revision,reused_transport,started_at_unix_ms) SELECT 'unclaimed',connection_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,caller_id,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,selection_source,default_revision,account_id,account_settings_revision,reused_transport,started_at_unix_ms FROM runtime_egress_attempts WHERE attempt_id='actual-attempt'`)
			}
			target := filepath.Join(t.TempDir(), "control.db")
			_, err = ExtractControl(ctx, ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout", ArchiveID: "archive"})
			if err != nil {
				t.Fatal(err)
			}
			out, err := sql.Open("sqlite", target)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			var count int
			if err = out.QueryRow(`SELECT count(*) FROM native_tool_once WHERE operation_id=? AND attempt_id='actual-attempt'`, id).Scan(&count); err != nil || count != 1 {
				t.Fatal("replay barrier lost")
			}
			want := 1
			if strings.HasPrefix(kind, "retired-") {
				want = 0
			}
			if err = out.QueryRow(`SELECT count(*) FROM native_tool_deliveries WHERE operation_id=?`, id).Scan(&count); err != nil || count != want {
				t.Fatalf("wrong delivery authority: %d %v", count, err)
			}
			if want == 1 {
				if err = out.QueryRow(`SELECT count(*) FROM native_tool_deliveries WHERE operation_id=? AND completed_at_unix_ms IS NULL`, id).Scan(&count); err != nil || count != 1 {
					t.Fatal("physical terminal fabricated")
				}
			}
			if err = db.QueryRow(`SELECT count(*) FROM runtime_egress_attempts WHERE attempt_id='actual-attempt' AND completed_at_unix_ms IS NULL`).Scan(&count); err != nil || count != 1 {
				t.Fatal("source history changed")
			}
		})
	}
}

func TestControlExtractionStructuralBoundary(t *testing.T) {
	for _, damage := range []string{"optional-fk", "optional-type", "optional-index"} {
		t.Run(damage, func(t *testing.T) {
			source, db := extractionFixture(t, 2)
			switch damage {
			case "optional-fk":
				nativeExec(t, db, `INSERT INTO runtime_raw_evidence_reveal_audits(envelope_id,exchange_id,actor_id,outcome,occurred_at_unix_ms) VALUES('missing','exchange','actor','succeeded',1)`)
			case "optional-type":
				var ddl string
				if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='runtime_raw_evidence_redaction'`).Scan(&ddl); err != nil {
					t.Fatal(err)
				}
				nativeExec(t, db, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=replace(sql,'salt BLOB','salt ANY') WHERE name='runtime_raw_evidence_redaction'; PRAGMA writable_schema=OFF; PRAGMA schema_version=100`)
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				changed, err := sql.Open("sqlite", source)
				if err != nil {
					t.Fatal(err)
				}
				nativeExec(t, changed, `INSERT INTO runtime_raw_evidence_redaction VALUES(1,'01234567890123456789012345678901');PRAGMA writable_schema=ON`)
				if _, err = changed.Exec(`UPDATE sqlite_schema SET sql=? WHERE name='runtime_raw_evidence_redaction'`, ddl); err != nil {
					t.Fatal(err)
				}
				nativeExec(t, changed, `PRAGMA writable_schema=OFF;PRAGMA schema_version=101`)
				if err = changed.Close(); err != nil {
					t.Fatal(err)
				}
			case "optional-index":
				nativeExec(t, db, `DROP INDEX runtime_usage_expiry`)
			}
			_, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "layout", ArchiveID: "archive"})
			if damage == "optional-fk" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("optional structural/type defect admitted")
			}
		})
	}
}

func TestControlExtractionMalformedUsageJSONWithSoundStoredColumns(t *testing.T) {
	source, db := extractionFixture(t, 2)
	payload := `{"status":"succeeded","source":"member","environmentId":"frozen-usage-boundary-unique"}`
	if _, err := db.Exec(`INSERT INTO runtime_usage_observations(exchange_id,capture_run_id,manual_capture_id,runtime_user_id,occurred_at_unix_ms,expires_at_unix_ms,observation_json) VALUES('usage','','','',1,100,?)`, payload); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately corrupt only JSON bytes in this synthetic record, retaining
	// record types, byte lengths, STORED generated values and all index entries.
	position := strings.Index(string(bytes), payload)
	if position < 0 || strings.Count(string(bytes), payload) != 1 {
		t.Fatal("fixture payload is not unique")
	}
	bytes[position] = '!'
	if err = os.WriteFile(source, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "layout", ArchiveID: "archive"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestControlExtractionRejectsReceivedInboundScopeMismatch(t *testing.T) {
	store, _, _ := nativeExecutionHandshakeFixture(t)
	source, db := legacyNativeFixture(t, store)
	nativeExec(t, db, `INSERT INTO native_tool_operations(operation_id,capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,rpc_kind,revision,state,decision_source,recording,retention_days,created_at_unix_ms,metadata_expires_at_unix_ms,terminal_reason)
 SELECT 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',capture_kind,capture_id,environment_id,environment_revision,environment_digest,connection_id,target_origin,method,'ping',1,'received','none',recording,retention_days,created_at_unix_ms,metadata_expires_at_unix_ms,'' FROM native_tool_operations LIMIT 1;
 INSERT INTO native_tool_events(operation_id,operation_revision,kind,occurred_at_unix_ms) SELECT operation_id,1,'received',created_at_unix_ms FROM native_tool_operations WHERE operation_id='eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee';
 INSERT INTO native_tool_inbound(session_id,rpc_id_digest,request_kind,request_digest,operation_id) SELECT session_id,randomblob(32),'tools_list',randomblob(32),'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee' FROM native_tool_sessions LIMIT 1;`)
	_, err := ExtractControl(context.Background(), ControlExtraction{SourcePath: source, TargetPath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "layout", ArchiveID: "archive"})
	if err == nil {
		t.Fatal("received inbound kind mismatch admitted")
	}
}
