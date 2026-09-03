package runtimepersistence

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

func TestRedactionSaltIsCreatedOnceAndSurvivesReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "runtime.db")

	store := openTestStore(t, databasePath)
	first, err := store.RawEvidenceRepository().RedactionSalt(
		context.Background(),
	)
	if err != nil {
		t.Fatalf("RedactionSalt() error = %v", err)
	}
	if len(first) != rawevidence.RedactionSaltBytes {
		t.Fatalf("salt length = %d, want %d",
			len(first), rawevidence.RedactionSaltBytes)
	}
	if bytes.Equal(first, make([]byte, rawevidence.RedactionSaltBytes)) {
		t.Fatal("salt is all zero")
	}
	again, err := store.RawEvidenceRepository().RedactionSalt(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("a second read created a different salt")
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	reopened := openTestStore(t, databasePath)
	afterReopen, err := reopened.RawEvidenceRepository().RedactionSalt(
		context.Background(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, afterReopen) {
		t.Fatal("reopening the database changed the redaction salt")
	}
}

func TestRedactionSaltDiffersBetweenDatabases(t *testing.T) {
	first, err := openTestStore(
		t, filepath.Join(t.TempDir(), "one.db"),
	).RawEvidenceRepository().RedactionSalt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := openTestStore(
		t, filepath.Join(t.TempDir(), "two.db"),
	).RawEvidenceRepository().RedactionSalt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("two databases share a redaction salt")
	}
}
