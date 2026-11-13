package productruntime

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/hostcontract"
)

func controlFingerprint(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT GLOB 'sqlite_*' ORDER BY name`)
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
	rows.Close()
	var fingerprint bytes.Buffer
	for _, table := range tables {
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM "` + table + `"`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&fingerprint, "%s=%d\n", table, count)
	}
	var schema string
	if err = db.QueryRow(`SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_schema ORDER BY type, name)`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	return fingerprint.String() + schema
}

// An install whose audit database predates script outcomes starts normally:
// the audit file is upgraded where it lies, and the storage layout and the
// control database that holds the configuration are left exactly as they were.
func TestRuntimeUpgradesAnOlderAuditDatabaseWithoutTouchingConfiguration(t *testing.T) {
	options := testOptions(t, hostcontract.Desktop(), &coordinatorDouble{})
	first := startTestRuntime(t, options)
	waitRecording(t, first)
	shutdownRuntime(t, first)

	root := options.Paths.DataDirectory()
	audit := filepath.Join(root, "observations.db")
	hasColumn := func() bool {
		db, err := sql.Open("sqlite", "file:"+audit+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var count int
		if err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('runtime_activities') WHERE name='script_outcomes_json'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count == 1
	}
	// Put the file back into the shape the previous release wrote.
	db, err := sql.Open("sqlite", audit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE runtime_activities DROP COLUMN script_outcomes_json`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if hasColumn() {
		t.Fatal("fixture still has the new column")
	}
	layoutBefore, err := os.ReadFile(filepath.Join(root, "storage-layout.json"))
	if err != nil {
		t.Fatal(err)
	}
	controlBefore := controlFingerprint(t, filepath.Join(root, "runtime.db"))

	second := startTestRuntime(t, options)
	waitRecording(t, second)
	if status := second.Status(); status.RecordingFailure != nil || status.StorageFailure != nil || status.RecordingMaintenanceFailure != nil {
		t.Fatalf("upgrade left a failure: %+v", status)
	}
	shutdownRuntime(t, second)
	if !hasColumn() {
		t.Fatal("audit database was not upgraded")
	}
	layoutAfter, err := os.ReadFile(filepath.Join(root, "storage-layout.json"))
	if err != nil || !bytes.Equal(layoutBefore, layoutAfter) {
		t.Fatalf("storage layout changed: %v", err)
	}
	if after := controlFingerprint(t, filepath.Join(root, "runtime.db")); after != controlBefore {
		t.Fatalf("control database changed:\n%s\n---\n%s", controlBefore, after)
	}
}
