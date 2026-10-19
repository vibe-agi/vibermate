package runtimedata

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
)

// Explicit construction boundary: repository staging is never a production
// backup input. Every frozen published column (including generated readback)
// and every sequence is copied/compared; there is no column intersection.
func freezePublishedBackupFixture(t *testing.T, root string) {
	t.Helper()
	input := filepath.Join(root, "runtime.db")
	if _, err := runtimepersistence.InspectSchema(context.Background(), input, runtimepersistence.DefaultBusyTimeout); err == nil {
		return
	}
	ddl, err := os.ReadFile("../runtimepersistence/schema/runtime_v3.sql")
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "published-fixture.db")
	db, err := sql.Open("sqlite", target)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ATTACH DATABASE ? AS staging`, input); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT name FROM main.sqlite_schema WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	// Staging-only tables have no published equivalent. Only the adapter's exact
	// default layout singleton is permitted, never retained authority/history.
	extra, err := db.Query(`SELECT name FROM staging.sqlite_schema WHERE type='table' AND name NOT IN(SELECT name FROM main.sqlite_schema WHERE type='table')`)
	if err != nil {
		t.Fatal(err)
	}
	var extras []string
	for extra.Next() {
		var table string
		if err = extra.Scan(&table); err != nil {
			t.Fatal(err)
		}
		extras = append(extras, table)
	}
	extra.Close()
	for _, table := range extras {
		var count int
		if table == "runtime_storage_state" {
			err = db.QueryRow(`SELECT count(*) FROM staging.runtime_storage_state WHERE singleton!=1 OR layout_id!='transitional-monolith' OR archive_epoch!=1 OR visibility_revision!=1`).Scan(&count)
		} else {
			err = db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM staging.%q`, table)).Scan(&count)
		}
		if err != nil || count != 0 {
			t.Fatalf("unrepresentable staging table %s: %d %v", table, count, err)
		}
	}
	for _, table := range tables {
		rows, err = db.Query(fmt.Sprintf(`PRAGMA main.table_info(%q)`, table))
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, fmt.Sprintf("%q", name))
		}
		rows.Close()
		publishedColumns := map[string]bool{}
		for _, column := range columns {
			publishedColumns[column] = true
		}
		// Default adapter-only coordinates represent the implicit original legacy
		// generation. Nondefault ownership/recording facts have no frozen equivalent
		// and must fail construction rather than disappear in projection.
		extraColumns, err := db.Query(fmt.Sprintf(`PRAGMA staging.table_info(%q)`, table))
		if err != nil {
			t.Fatal(err)
		}
		type omitted struct{ name, def string }
		var omittedColumns []omitted
		for extraColumns.Next() {
			var cid, nn, pk int
			var name, kind string
			var def sql.NullString
			if err = extraColumns.Scan(&cid, &name, &kind, &nn, &def, &pk); err != nil {
				t.Fatal(err)
			}
			quoted := fmt.Sprintf("%q", name)
			if !publishedColumns[quoted] {
				value := "NULL"
				if def.Valid {
					value = def.String
				}
				omittedColumns = append(omittedColumns, omitted{quoted, value})
			}
		}
		extraColumns.Close()
		for _, column := range omittedColumns {
			var count int
			if err = db.QueryRow(fmt.Sprintf(`SELECT count(*) FROM staging.%q WHERE %s IS NOT (%s)`, table, column.name, column.def)).Scan(&count); err != nil || count != 0 {
				t.Fatalf("nondefault staging-only column %s.%s: %d %v", table, column.name, count, err)
			}
		}
		cols := strings.Join(columns, ",")
		if _, err = db.Exec(fmt.Sprintf(`DELETE FROM main.%q; INSERT INTO main.%q(%s) SELECT %s FROM staging.%q`, table, table, cols, cols, table)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`DETACH DATABASE staging`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	// Compare all emitted columns, not only those inserted. New adapter-only
	// columns are deliberately excluded from this published comparison, never
	// substituted for a published field.
	expected := snapshotRowsAtFile(t, input, tables)
	actual := snapshotRowsAtFile(t, target, tables)
	if !slices.Equal(expected, actual) {
		t.Fatal("published fixture changed an installed column/generated value or sequence")
	}
	if err = os.Chmod(target, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(target, input); err != nil {
		t.Fatal(err)
	}
	if _, err = runtimepersistence.ValidateOfflineDatabase(context.Background(), input); err != nil {
		t.Fatal(err)
	}
}

func snapshotRowsAtFile(t *testing.T, path string, tables []string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out []string
	// The published DDL defines every comparison column, including generated ones.
	reference, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer reference.Close()
	ddl, err := os.ReadFile("../runtimepersistence/schema/runtime_v3.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reference.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		rows, err := reference.Query(fmt.Sprintf(`PRAGMA table_xinfo(%q)`, table))
		if err != nil {
			t.Fatal(err)
		}
		var cols []string
		for rows.Next() {
			var cid, nn, pk, hidden int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &nn, &def, &pk, &hidden); err != nil {
				t.Fatal(err)
			}
			cols = append(cols, fmt.Sprintf("%q", name))
		}
		rows.Close()
		rows, err = db.Query(fmt.Sprintf(`SELECT %s FROM %q`, strings.Join(cols, ","), table))
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range values {
				ptrs[i] = &values[i]
			}
			if err = rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("%s:%#v", table, values))
		}
		rows.Close()
	}
	slices.Sort(out)
	return out
}

// Reads use the real compact control repositories, on an independent extraction
// for a frozen rollback result. Neither the restored monolith nor its WAL is
// changed, upgraded, or republished by these assertions.
func openBackupControlReader(t *testing.T, root string) *runtimepersistence.ControlStore {
	t.Helper()
	path := filepath.Join(root, "runtime.db")
	layout, err := ReadStorageLayout(root)
	if err != nil {
		temp, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		copied := filepath.Join(temp, "source.db")
		for _, suffix := range []string{"", "-wal"} {
			data, err := os.ReadFile(path + suffix)
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(copied+suffix, data, 0600); err != nil {
				t.Fatal(err)
			}
		}
		path = filepath.Join(temp, "control.db")
		layout.ID = "backup-assertion"
		if _, err = runtimepersistence.ExtractControl(context.Background(), runtimepersistence.ControlExtraction{SourcePath: copied, TargetPath: path, LayoutID: layout.ID, ArchiveID: "backup-assertion-archive"}); err != nil {
			t.Fatal(err)
		}
	}
	store, err := runtimepersistence.OpenControl(context.Background(), runtimepersistence.ControlOptions{DatabasePath: path, LayoutID: layout.ID, BusyTimeout: runtimepersistence.DefaultBusyTimeout, CommitReconcileTimeout: runtimepersistence.DefaultCommitReconcileTimeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Shutdown(context.Background()) })
	return store
}
func backupHistoryPath(t *testing.T, root string) string {
	t.Helper()
	if layout, err := ReadStorageLayout(root); err == nil && len(layout.Archives) > 0 {
		return filepath.Join(root, layout.Archives[0].RelativeDirectory, "runtime.db")
	}
	return filepath.Join(root, "runtime.db")
}
func readBackupRawBody(t *testing.T, root, id string) []byte {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+backupHistoryPath(t, root)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var manifest []byte
	if err = db.QueryRow(`SELECT b.chunk_manifest FROM runtime_raw_evidence_envelopes e JOIN runtime_evidence_bodies b ON b.digest=e.stored_body_digest WHERE e.envelope_id=?`, id).Scan(&manifest); err != nil {
		t.Fatal(err)
	}
	var out []byte
	for len(manifest) > 0 {
		if len(manifest) < 32 {
			t.Fatal("invalid body manifest")
		}
		var codec string
		var payload []byte
		if err = db.QueryRow(`SELECT codec,payload FROM runtime_evidence_chunks WHERE digest=?`, manifest[:32]).Scan(&codec, &payload); err != nil {
			t.Fatal(err)
		}
		if codec == "zstd" {
			reader, err := zstd.NewReader(nil)
			if err != nil {
				t.Fatal(err)
			}
			payload, err = reader.DecodeAll(payload, nil)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
		} else if codec != "identity" {
			t.Fatal("unknown codec")
		}
		out = append(out, payload...)
		manifest = manifest[32:]
	}
	return bytes.Clone(out)
}

func snapshotBackupTable(t *testing.T, path, table string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(fmt.Sprintf(`SELECT * FROM %q`, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range ptrs {
			ptrs[i] = &values[i]
		}
		if err = rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%#v", values))
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(out)
	return out
}
