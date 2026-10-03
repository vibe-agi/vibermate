package runtimepersistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFrozenRevision1SchemaBytes(t *testing.T) {
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(schemaV1SQL))); got != "393b7d67f8ffd1952fc516a150e1a2a68707b67536fbc2e7f5a7f17208424cb1" {
		t.Fatal("frozen revision-one schema changed")
	}
}

func TestSchemaSwitchFrozenV2LayoutsAndTamper(t *testing.T) {
	for _, layout := range []string{"fresh", "migrated"} {
		t.Run(layout, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.db")
			db := openSQLiteForTest(t, path)
			if layout == "fresh" {
				nativeExec(t, db, schemaV2SQL)
			} else {
				nativeExec(t, db, schemaV1SQL)
				nativeExec(t, db, migration001To002SQL)
			}
			nativeExec(t, db, `INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',2,'2026-10-03T00:00:00Z')`)
			db.Close()
			state, err := InspectSchema(context.Background(), path, time.Second)
			if err != nil || state.Revision != 2 {
				t.Fatalf("known %s layout rejected: %v", layout, err)
			}
			db = openSQLiteForTest(t, path)
			// Removing all double quotes would incorrectly treat this changed string
			// literal as equivalent. Exact trusted layouts must reject it.
			nativeExec(t, db, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql=replace(sql,'''received''','''"received"''') WHERE type='table' AND name='native_tool_operations'`)
			db.Close()
			if _, err := InspectSchema(context.Background(), path, time.Second); !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatal("changed SQL string literal accepted")
			}
		})
	}
}

func TestInspectSchemaObservesCommittedWALUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	shutdownTestStore(t, openTestStore(t, path))
	db := openSQLiteForTest(t, path)
	if _, err := db.Exec("PRAGMA wal_autocheckpoint=0; UPDATE runtime_metadata SET schema_revision=99"); err != nil {
		t.Fatal(err)
	}
	wantFiles := map[string][]byte{}
	wantModes := map[string]os.FileMode{}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		value, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		wantFiles[name] = value
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		wantModes[name] = info.Mode()
	}
	if _, err := InspectSchema(context.Background(), path, DefaultBusyTimeout); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("ignored committed WAL revision: %v", err)
	}
	for _, name := range []string{path, path + "-wal"} {
		value, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(value, wantFiles[name]) {
			t.Fatal("inspection mutated persistent SQLite bytes")
		}
	}
	for name, mode := range wantModes {
		info, err := os.Stat(name)
		if err != nil || info.Mode() != mode {
			t.Fatal("inspection changed SQLite file permissions")
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, []string{"runtime.db", "runtime.db-shm", "runtime.db-wal"}) {
		t.Fatal("inspection created a file")
	}
}

func TestInspectSchemaCommittedWALWithoutSHM(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "original.db")
	shutdownTestStore(t, openTestStore(t, original))
	db := openSQLiteForTest(t, original)
	if _, err := db.Exec("PRAGMA wal_autocheckpoint=0; UPDATE runtime_metadata SET schema_revision=99"); err != nil {
		t.Fatal(err)
	}
	// A crash-style stopped fixture retains committed WAL but no SHM. Never
	// unlink shared memory underneath the synthetic writer that created it.
	stopped := filepath.Join(root, "stopped")
	if err := os.Mkdir(stopped, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stopped, "runtime.db")
	for _, suffix := range []string{"", "-wal"} {
		encoded, err := os.ReadFile(original + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path+suffix, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := InspectSchema(context.Background(), path, DefaultBusyTimeout); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("ignored committed WAL without SHM: %v", err)
	}
	entries, err := os.ReadDir(stopped)
	if err != nil || len(entries) != 2 {
		t.Fatal("inspection created a source sidecar")
	}
}

func TestInspectSchemaSourceChangesAndTemporaryCleanup(t *testing.T) {
	for _, scenario := range []string{"database_after_read", "wal_appears_after_read", "empty_wal_changes_after_read", "wal_source_mutation", "wal_source_mutation_before_copy", "wal_copy_failure", "wal_cancel"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "runtime.db")
			shutdownTestStore(t, openTestStore(t, path))
			if strings.HasPrefix(scenario, "wal_") && scenario != "wal_appears_after_read" {
				db := openSQLiteForTest(t, path)
				if _, err := db.Exec("PRAGMA wal_autocheckpoint=0; UPDATE runtime_metadata SET initialized_at='2026-10-03T01:00:00Z'"); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "empty_wal_changes_after_read" {
				if err := os.WriteFile(path+"-wal", nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			temporaryRoot := filepath.Join(root, "private")
			if err := os.Mkdir(temporaryRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			runner := newSchemaInspectionRunner()
			readSchema := runner.readSchema
			copyFile := runner.copyFile
			runner.temporaryDirectory = func() (string, error) { return os.MkdirTemp(temporaryRoot, "inspection-") }
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if scenario == "database_after_read" || scenario == "wal_appears_after_read" || scenario == "empty_wal_changes_after_read" {
				runner.readSchema = func(ctx context.Context, path string, timeout time.Duration, immutable bool) (SchemaState, error) {
					state, err := readSchema(ctx, path, timeout, immutable)
					switch scenario {
					case "database_after_read":
						if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Hour)); err != nil {
							t.Fatal(err)
						}
					default:
						if err := os.WriteFile(path+"-wal", []byte("synthetic new WAL"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					return state, err
				}
			} else {
				injected := false
				runner.copyFile = func(ctx context.Context, source, target string) (schemaInspectionFile, error) {
					if !injected && scenario == "wal_source_mutation_before_copy" {
						injected = true
						if err := os.Chtimes(source, time.Now(), time.Now().Add(time.Hour)); err != nil {
							t.Fatal(err)
						}
					}
					file, err := copyFile(ctx, source, target)
					if !injected {
						injected = true
						switch scenario {
						case "wal_copy_failure":
							return schemaInspectionFile{}, errors.New("synthetic copy failure")
						case "wal_cancel":
							cancel()
						case "wal_source_mutation":
							if err := os.Chtimes(source, time.Now(), time.Now().Add(time.Hour)); err != nil {
								t.Fatal(err)
							}
						}
					}
					return file, err
				}
			}
			if _, err := runner.inspect(ctx, path, DefaultBusyTimeout); !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatalf("changed source accepted: %v", err)
			}
			entries, err := os.ReadDir(temporaryRoot)
			if err != nil || len(entries) != 0 {
				t.Fatal("private inspection copy leaked")
			}
		})
	}
}

func TestSchemaInspectionEmbeddedSQLiteVersion(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var version string
	if err := database.QueryRow("SELECT sqlite_version()").Scan(&version); err != nil {
		t.Fatal(err)
	}
	t.Logf("embedded SQLite version: %s", version)
}

func TestInspectSchemaReadOnlyAndFailClosed(t *testing.T) {
	for _, scenario := range []string{"absent", "zero", "empty", "v1", "v2", "foreign", "foreign_reserved_prefix", "forged_revision2", "revision99", "missing_table", "missing_index", "forged_metadata", "extra_table", "hidden_extra_table"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "runtime.db")
			if scenario == "zero" {
				if err := os.WriteFile(path, nil, 0o640); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "absent" && scenario != "zero" {
				if scenario != "empty" && scenario != "foreign" && scenario != "foreign_reserved_prefix" && scenario != "forged_metadata" && scenario != "v1" && scenario != "forged_revision2" {
					shutdownTestStore(t, openTestStore(t, path))
				}
				db := openSQLiteForTest(t, path)
				statement := "PRAGMA user_version"
				switch scenario {
				case "v1", "forged_revision2":
					statement = schemaV1SQL + `; INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',1,'2026-10-03T00:00:00Z')`
					if scenario == "forged_revision2" {
						statement += `; UPDATE runtime_metadata SET schema_revision=2`
					}
				case "foreign":
					statement = "CREATE TABLE foreign_data(value TEXT)"
				case "foreign_reserved_prefix":
					statement = "CREATE TABLE sqliteXforeign(value TEXT)"
				case "revision99":
					statement = "UPDATE runtime_metadata SET schema_revision=99"
				case "missing_table":
					statement = "DROP TABLE runtime_usage_policy"
				case "missing_index":
					statement = "DROP INDEX connection_events_observed_index"
				case "forged_metadata":
					statement = "CREATE TABLE runtime_metadata(singleton INTEGER,schema_identity TEXT,schema_revision INTEGER,initialized_at TEXT); INSERT INTO runtime_metadata VALUES(1,'vibermate-runtime',1,'2026-10-03T00:00:00Z')"
				case "extra_table":
					statement = "CREATE TABLE extra_data(value TEXT)"
				case "hidden_extra_table":
					statement = "CREATE TABLE sqliteXextra(value TEXT)"
				}
				if scenario == "missing_index" {
					var index string
					if err := db.QueryRow("SELECT name FROM sqlite_schema WHERE type='index' AND sql IS NOT NULL LIMIT 1").Scan(&index); err != nil {
						t.Fatal(err)
					}
					statement = fmt.Sprintf("DROP INDEX %q", index)
				}
				if _, err := db.Exec(statement); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			entriesBefore, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			beforeInfo, _ := os.Stat(path)
			state, err := InspectSchema(ctx, path, DefaultBusyTimeout)
			switch scenario {
			case "absent", "zero", "empty":
				if err != nil || state != (SchemaState{}) {
					t.Fatalf("empty schema = %+v, %v", state, err)
				}
			case "v1":
				if err != nil || state.Identity != "vibermate-runtime" || state.Revision != 1 || state.InitializedAt == "" {
					t.Fatalf("schema = %+v, %v", state, err)
				}
			case "v2":
				if err != nil || state.Revision != 2 {
					t.Fatalf("actual v2 = %+v %v", state, err)
				}
			default:
				if !errors.Is(err, ErrUnsupportedSchema) {
					t.Fatalf("malformed schema accepted: %v", err)
				}
			}
			after, _ := os.ReadFile(path)
			entriesAfter, readErr := os.ReadDir(filepath.Dir(path))
			if readErr != nil || len(entriesBefore) != len(entriesAfter) {
				t.Fatal("schema inspection created a file")
			}
			afterInfo, _ := os.Stat(path)
			if !bytes.Equal(before, after) || (beforeInfo == nil) != (afterInfo == nil) || beforeInfo != nil && (beforeInfo.Mode() != afterInfo.Mode() || !beforeInfo.ModTime().Equal(afterInfo.ModTime())) {
				t.Fatal("schema inspection modified source")
			}
		})
	}
}
