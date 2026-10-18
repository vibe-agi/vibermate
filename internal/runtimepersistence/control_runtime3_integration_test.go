package runtimepersistence

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/nativetool"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPublishedControlExtractionPreservesUsedRuntime3AndLegacyModes(t *testing.T) {
	for _, shape := range []string{"v1", "v2", "fresh3", "v2+002", "v1+001+002"} {
		t.Run(shape, func(t *testing.T) {
			var source string
			revision := int64(2)
			var db *sql.DB
			if shape == "v1" {
				f := populatedV1MigrationFixture(t)
				source = filepath.Join(f.source, "runtime.db")
				revision = 1
				db = openSQLiteForTest(t, source)
			} else {
				profile := egressprofile.Direct()
				profile.ID = "profile.used-source"
				profile.DisplayName = "Preserved native egress"
				profile.Policy.Proxy = egressnetwork.ProxyPolicy{Kind: egressnetwork.ProxySOCKS5, Endpoint: "127.0.0.1:1080"}
				store, id, rev := nativeFixtureOnStore(t, environment.ToolPolicyReview, environment.ContentRecordingPolicy{Mode: environment.ContentRecordingOff}, true, nil, nil, profile)
				repo := nativeFullRepository(t, store)
				now := time.Now().UTC().Truncate(time.Millisecond)
				nativeAllow(t, repo, id, now)
				if _, err := repo.Start(t.Context(), id, "used-native-attempt", rev, now); err != nil {
					t.Fatal(err)
				}
				if err := repo.Finish(t.Context(), id, nativetool.TerminalInput{Outcome: "uncertain", Reason: "execution_uncertain", ObservedAt: now}); err != nil {
					t.Fatal(err)
				}
				source, db = legacyNativeFixture(t, store)
				nativeExec(t, db, `INSERT INTO provider_accounts(account_id,display_name,credential_origin,endpoint_associations,association_revision,note,note_revision,settings_revision,egress_profile_json,automatic_refresh,realm_id,driver_ref,secret_reference,state,revision,created_at_unix_ms,updated_at_unix_ms) VALUES('preserved','Preserved','https://chatgpt.com','[]',3,'exact note',9,8,'{}',0,'openai.chatgpt','static_header','secret://provider-account/preserved','active',7,1,9)`)
				if shape == "v1+001+002" {
					db.Close()
					rehouseRuntime2Alter(t, source)
					db = openSQLiteForTest(t, source)
				}
			}
			mode := "standard"
			if shape != "v1" && shape != "v2" {
				nativeExec(t, db, migration002To003SQL)
				nativeExec(t, db, `UPDATE runtime_metadata SET schema_revision=3`)
				revision = 3
				if shape == "fresh3" {
					db.Close()
					rehousePublishedRuntime3(t, source)
					db = openSQLiteForTest(t, source)
				}
				mode = "daybreak_blue"
				if shape == "v1+001+002" {
					mode = "daybreak_red"
				}
				nativeExec(t, db, `UPDATE provider_accounts SET credential_origin='https://chatgpt.com',realm_id='openai.chatgpt',driver_ref='static_header',cyber_access_program=?`, mode)
			}
			// Every preexisting account field, native table and sequence is independently retained.
			columns := revision2AssertionColumns(t, db)
			accountColumns := columns["provider_accounts"]
			accountBefore := nativeFingerprint(t, db, map[string][]string{"provider_accounts": accountColumns})
			var nativeBefore string
			if revision != 1 {
				nativeBefore = fmt.Sprint(nativeFingerprint(t, db, map[string][]string{"native_tool_once": columns["native_tool_once"], "native_tool_approvals": columns["native_tool_approvals"], "native_tool_inbound": columns["native_tool_inbound"], "native_tool_binding_egress": columns["native_tool_binding_egress"]}))
			}
			// Stopped retained WAL: the source remains open and no checkpoint occurs during extraction.
			nativeExec(t, db, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE provider_accounts SET note=note`)
			before := map[string][]byte{}
			for _, suffix := range []string{"", "-wal"} {
				data, err := os.ReadFile(source + suffix)
				if err != nil {
					t.Fatal(err)
				}
				before[suffix] = data
			}
			target := filepath.Join(t.TempDir(), "control.db")
			proof, err := ExtractControl(t.Context(), ControlExtraction{SourcePath: source, TargetPath: target, LayoutID: "layout", ArchiveID: "legacy"})
			if err != nil {
				t.Fatal(err)
			}
			if proof.Source.Identity != "vibermate-runtime" || proof.Source.Revision != revision {
				t.Fatalf("wrong source %+v", proof.Source)
			}
			for suffix, want := range before {
				got, err := os.ReadFile(source + suffix)
				if err != nil || !bytes.Equal(want, got) {
					t.Fatalf("source %s mutated: %v", suffix, err)
				}
			}
			out := openSQLiteForTest(t, target)
			if got := nativeFingerprint(t, out, map[string][]string{"provider_accounts": accountColumns}); !reflect.DeepEqual(got, accountBefore) {
				t.Fatal("original account fields changed")
			}
			var actualMode string
			if err = out.QueryRow(`SELECT cyber_access_program FROM provider_accounts LIMIT 1`).Scan(&actualMode); err != nil || actualMode != mode {
				t.Fatalf("mode %q want %q: %v", actualMode, mode, err)
			}
			if revision != 1 && fmt.Sprint(nativeFingerprint(t, out, map[string][]string{"native_tool_once": columns["native_tool_once"], "native_tool_approvals": columns["native_tool_approvals"], "native_tool_inbound": columns["native_tool_inbound"], "native_tool_binding_egress": columns["native_tool_binding_egress"]})) != nativeBefore {
				t.Fatal("native authority changed")
			}
			if revision != 1 {
				for _, table := range []string{"native_tool_once", "native_tool_approvals", "native_tool_inbound"} {
					var n int
					if err = out.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n == 0 {
						t.Fatalf("empty native preservation fixture %s: %v", table, err)
					}
				}
				var unknown int
				if err = out.QueryRow(`SELECT count(*) FROM native_tool_deliveries WHERE attempt_id='used-native-attempt' AND byte_counts_known=0`).Scan(&unknown); err != nil || unknown != 1 {
					t.Fatalf("unknown delivery bytes changed: %d %v", unknown, err)
				}
				if err = out.QueryRow(`SELECT count(*) FROM native_tool_operations WHERE state='uncertain' AND bytes_in IS NULL AND bytes_out IS NULL`).Scan(&unknown); err != nil || unknown != 1 {
					t.Fatalf("unknown native bytes changed: %d %v", unknown, err)
				}
			}
			out.Close()
			digest, count, err := CertifyStagedControlProjection(t.Context(), target, "layout", revision)
			if err != nil || digest != proof.ProjectionDigest || count != proof.Rows {
				t.Fatalf("independent certification %d %v", count, err)
			}
			if revision == 3 {
				out = openSQLiteForTest(t, target)
				nativeExec(t, out, `UPDATE provider_accounts SET cyber_access_program='standard'`)
				out.Close()
				changed, _, err := CertifyStagedControlProjection(t.Context(), target, "layout", revision)
				if err != nil || changed == proof.ProjectionDigest {
					t.Fatalf("mode missing from typed proof: %v", err)
				}
			}
		})
	}
}

// Reuse the published rehouse pattern, preserving every writable column and sequence.
func rehousePublishedRuntime3(t *testing.T, path string) {
	rehousePublishedDDL(t, path, schemaRuntimeV3SQL)
}
func rehouseRuntime2Alter(t *testing.T, path string) {
	rehousePublishedDDL(t, path, schemaV1SQL+"\n"+migration001To002SQL)
}
func rehousePublishedDDL(t *testing.T, path, ddl string) {
	t.Helper()
	dst := path + ".fresh3"
	db := openSQLiteForTest(t, dst)
	nativeExec(t, db, `PRAGMA foreign_keys=OFF`)
	nativeExec(t, db, ddl)
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
	columns := nativeLegacyColumns(t, db)
	for table, cols := range columns {
		if table != "sqlite_sequence" {
			nativeExec(t, db, `DELETE FROM main.`+quoteMigrationIdentifier(table)+`; INSERT INTO main.`+quoteMigrationIdentifier(table)+` SELECT `+joinMigrationColumns(cols)+` FROM old.`+quoteMigrationIdentifier(table))
		}
	}
	nativeExec(t, db, `DELETE FROM main.sqlite_sequence;INSERT INTO main.sqlite_sequence SELECT * FROM old.sqlite_sequence`)
	for _, o := range objects {
		if o.kind == "trigger" {
			nativeExec(t, db, o.statement)
		}
	}
	nativeExec(t, db, `DETACH DATABASE old`)
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(dst, path); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedRuntime3ArchiveLayoutsAreExplicit(t *testing.T) {
	for _, ddl := range []string{schemaRuntimeV3SQL, schemaV2SQL + "\n" + migration002To003SQL, schemaV1SQL + "\n" + migration001To002SQL + "\n" + migration002To003SQL} {
		options, _ := inspectionSourcesFixture(t)
		path := filepath.Join(options.Root, "archives/legacy/runtime.db")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		nativeExec(t, db, ddl)
		nativeExec(t, db, `INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',3,'2026-10-03T00:00:00Z')`)
		db.Close()
		options.Sources[0].Database.Revision = 3
		catalog, err := NewInspectionCatalog(options)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := catalog.acquire(context.Background())
		if err != nil {
			catalog.Close(context.Background())
			t.Fatal(err)
		}
		lease.close()
		catalog.Close(context.Background())
	}

}

func TestPublishedSourceInspectionRefusesCombinedAdapterUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	store := openTestStore(t, path)
	shutdownTestStore(t, store)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = InspectSchema(t.Context(), path, DefaultBusyTimeout); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("private source inspection: %v", err)
	}
	if _, err = ExtractControl(t.Context(), ControlExtraction{SourcePath: path, TargetPath: filepath.Join(t.TempDir(), "control.db"), LayoutID: "layout", ArchiveID: "legacy"}); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("private source extraction: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("private source changed: %v", err)
	}
}
