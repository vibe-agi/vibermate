package runtimepersistence

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/workspacedefault"
)

func TestWorkspaceEnvironmentDefaultCASDeleteAndReopen(t *testing.T) {
	t.Parallel()
	databasePath := filepath.Join(t.TempDir(), "runtime.db")
	key := workspaceDefaultKey(t)
	now := time.Date(2026, 8, 8, 10, 15, 0, 0, time.UTC)
	first := openTestStore(t, databasePath)
	repository := first.WorkspaceDefaultRepository()
	candidate := workspacedefault.Record{
		Key: key, EnvironmentID: environment.EnvironmentID("work"), Revision: 1, UpdatedAt: now,
	}
	result, err := repository.Write(context.Background(), 0, candidate)
	if err != nil || result.Outcome != workspacedefault.CommitCommitted || result.Record != candidate {
		t.Fatalf("create result = %+v, error %v", result, err)
	}
	stale := candidate
	stale.EnvironmentID = "personal"
	result, err = repository.Write(context.Background(), 0, stale)
	if err != nil || result.Outcome != workspacedefault.CommitConflict || result.Actual != 1 {
		t.Fatalf("stale result = %+v, error %v", result, err)
	}
	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	second := openTestStore(t, databasePath)
	loaded, exists, err := second.WorkspaceDefaultRepository().Load(context.Background(), key)
	if err != nil || !exists || loaded != candidate {
		t.Fatalf("reopened record = %+v, exists %t, error %v", loaded, exists, err)
	}
	result, err = second.WorkspaceDefaultRepository().Delete(context.Background(), key, 1)
	if err != nil || result.Outcome != workspacedefault.CommitCommitted || !result.Deleted {
		t.Fatalf("delete result = %+v, error %v", result, err)
	}
	if _, exists, err := second.WorkspaceDefaultRepository().Load(context.Background(), key); err != nil || exists {
		t.Fatalf("deleted record = exists %t, error %v", exists, err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func workspaceDefaultKey(t *testing.T) workspacedefault.Key {
	t.Helper()
	encoded := func(value byte) string {
		return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	}
	key, err := workspacedefault.NewKey(encoded(3), encoded(4))
	if err != nil {
		t.Fatal(err)
	}
	return key
}
