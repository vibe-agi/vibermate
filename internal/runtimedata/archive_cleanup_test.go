package runtimedata

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/instanceguard"
)

func TestArchiveCleanupReceiptRequiresOwnedPrivateDurableState(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	guard, err := instanceguard.Acquire(filepath.Join(root, lockName))
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Release()
	receipt := RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: "layout", SourceSet: strings.Repeat("a", 64), TargetRevision: 2, SourceGeneration: 3, CutoffMillis: 1234, Phase: "select", CountsExact: true}
	if err = WriteRecordingCleanupReceipt(t.Context(), root, nil, receipt); err == nil {
		t.Fatal("unowned cleanup receipt published")
	}
	if err = WriteRecordingCleanupReceipt(t.Context(), root, guard, receipt); err != nil {
		t.Fatal("owned cleanup intent unavailable:", err)
	}
	got, exists, err := ReadRecordingCleanupReceipt(root)
	if err != nil || !exists || got != receipt {
		t.Fatalf("cleanup receipt lost: %+v %v %v", got, exists, err)
	}
	info, err := os.Lstat(filepath.Join(root, RecordingCleanupReceiptName))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("nonprivate receipt: %v", err)
	}
	receipt.Phase = "delete"
	receipt.CountsExact = false
	if err = WriteRecordingCleanupReceipt(t.Context(), root, guard, receipt); err != nil {
		t.Fatal(err)
	}
	got, _, err = ReadRecordingCleanupReceipt(root)
	if err != nil || got.CountsExact {
		t.Fatal("unknown crash-gap counts became exact")
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err = RemoveRecordingCleanupReceipt(canceled, root, guard); err == nil {
		t.Fatal("canceled cleanup retired receipt")
	}
	if _, exists, err = ReadRecordingCleanupReceipt(root); err != nil || !exists {
		t.Fatal("receipt disappeared before completion")
	}
	if err = RemoveRecordingCleanupReceipt(t.Context(), root, guard); err != nil {
		t.Fatal(err)
	}
	if _, exists, err = ReadRecordingCleanupReceipt(root); err != nil || exists {
		t.Fatal("completed receipt did not retire")
	}
}
