package runtimepersistence

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// After a deliberate archive clear, freelist pages must be returned to the OS.
// DELETE alone leaves the file large; reclaimEvidencePages is the product path
// that makes "clear" feel like reclaim, not just logical emptiness.
func TestClearEvidenceReclaimsSQLitePages(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "clear-vacuum.db")
	store := openTestStore(t, databasePath)
	for index := range 8 {
		seedCaptureGraph(t, store, "managed_run", fmt.Sprintf("run-vacuum-%d", index), 12)
	}
	var freelistBefore int
	if err := store.database.QueryRow(`PRAGMA freelist_count`).Scan(&freelistBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClearEvidence(context.Background()); err != nil {
		t.Fatal(err)
	}
	var freelistAfter int
	if err := store.database.QueryRow(`PRAGMA freelist_count`).Scan(&freelistAfter); err != nil {
		t.Fatal(err)
	}
	if freelistAfter != 0 {
		t.Fatalf("freelist_count after clear = %d (before delete freelist=%d); pages were not reclaimed", freelistAfter, freelistBefore)
	}
	shutdownTestStore(t, store)
}
