package runtimepersistence

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestOfflineValidationRetainedWALLeavesSourceBytesUntouched(t *testing.T) {
	root := t.TempDir()
	builder := filepath.Join(root, "builder.db")
	createPublishedRuntime3InspectionFixture(t, builder)
	db, err := sql.Open("sqlite", builder)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; UPDATE runtime_metadata SET initialized_at='2026-10-18T00:00:00Z'`); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "stopped.db")
	originals := map[string][]byte{}
	for _, suffix := range []string{"", "-wal"} {
		encoded, err := os.ReadFile(builder + suffix)
		if err != nil {
			t.Fatal(err)
		}
		originals[suffix] = encoded
		if err = os.WriteFile(target+suffix, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	state, err := ValidateOfflineDatabase(context.Background(), target)
	if err != nil || state.InitializedAt != "2026-10-18T00:00:00Z" {
		t.Fatalf("committed WAL state %+v: %v", state, err)
	}
	for suffix, want := range originals {
		got, err := os.ReadFile(target + suffix)
		if err != nil || string(got) != string(want) {
			t.Fatalf("offline validation changed source %q: %v", suffix, err)
		}
	}
	if _, err := os.Lstat(target + "-shm"); !os.IsNotExist(err) {
		t.Fatalf("created source SHM: %v", err)
	}
}
