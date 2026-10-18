package runtimedata

import (
	"context"
	"github.com/vibe-agi/vibermate/internal/runtimepersistence"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRecordingMaintenanceCertificateRequiresAllCleanFilesAndOriginalProvenance(t *testing.T) {
	owned := storageCutoverFixture(t, 2)
	layout, err := PrepareStorageOwned(t.Context(), owned, time.Now().UTC(), nil)
	if err != nil {
		t.Fatal(err)
	}
	current, err := runtimepersistence.OpenObservations(t.Context(), runtimepersistence.ObservationOptions{DatabasePath: filepath.Join(owned.Directory, "observations.db"), LayoutID: layout.ID, ArchiveID: "current", BusyTimeout: time.Second, CommitReconcileTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = current.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	receipt := RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: layout.ID, SourceSet: strings.Repeat("a", 64), TargetRevision: 1, SourceGeneration: 2, CutoffMillis: time.Now().UnixMilli(), Phase: "verify", CountsExact: true}
	wal := filepath.Join(owned.Directory, layout.Archives[0].RelativeDirectory, "runtime.db-wal")
	if err = os.WriteFile(wal, []byte("retained deleted bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = CertifyRecordingMaintenance(t.Context(), owned.Directory, owned.Runtime, layout, receipt); err == nil {
		t.Fatal("retained WAL certified as physical completion")
	}
	if err = os.Remove(wal); err != nil {
		t.Fatal(err)
	}
	provenance := filepath.Join(owned.Directory, layout.Archives[0].RelativeDirectory, "archive-manifest.json")
	original, err := os.ReadFile(provenance)
	if err != nil {
		t.Fatal(err)
	}
	if err = CertifyRecordingMaintenance(t.Context(), owned.Directory, owned.Runtime, layout, receipt); err != nil {
		t.Fatal(err)
	}
	if after, err := os.ReadFile(provenance); err != nil || string(after) != string(original) {
		t.Fatal("original provenance rewritten")
	}
	if _, err = os.Stat(filepath.Join(owned.Directory, RecordingMaintenanceCertificateName)); err != nil {
		t.Fatal("missing distinct maintained-byte certificate", err)
	}
	if err = os.Remove(provenance); err != nil {
		t.Fatal(err)
	}
	if err = CertifyRecordingMaintenance(t.Context(), owned.Directory, owned.Runtime, layout, receipt); err == nil {
		t.Fatal("missing provenance certified")
	}
}

func TestRecordingCleanupRetiresOnlyOwnedMetadataTempsAfterDurableReceipt(t *testing.T) {
	owned := storageCutoverFixture(t, 0)
	receipt := RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: "layout", SourceSet: strings.Repeat("a", 64), TargetRevision: 1, SourceGeneration: 2, CutoffMillis: 123, Phase: "select", CountsExact: true}
	path := filepath.Join(owned.Directory, ".recording-cleanup-12345")
	if err := os.WriteFile(path, []byte(`{"schema":`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RetireRecordingCleanupArtifacts(t.Context(), owned.Directory, owned.Runtime, receipt); err == nil {
		t.Fatal("metadata retirement lacked durable matching receipt")
	}
	if err := WriteRecordingCleanupReceipt(t.Context(), owned.Directory, owned.Runtime, receipt); err != nil {
		t.Fatal(err)
	}
	if err := RetireRecordingCleanupArtifacts(t.Context(), owned.Directory, owned.Runtime, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("known interrupted metadata temp survived", err)
	}
	unknown := filepath.Join(owned.Directory, ".recording-certificate-unknown")
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := RetireRecordingCleanupArtifacts(t.Context(), owned.Directory, owned.Runtime, receipt); err == nil {
		t.Fatal("unexpected reserved name silently accepted")
	}
	if bytes, err := os.ReadFile(unknown); err != nil || string(bytes) != "keep" {
		t.Fatal("unknown artifact swept")
	}
}

func TestRecordingCleanupArtifactsRefuseOversizedOrUnownedEntries(t *testing.T) {
	for _, mode := range []string{"oversized", "nonprivate", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			owned := storageCutoverFixture(t, 0)
			receipt := RecordingCleanupReceipt{Schema: "vibermate-recording-cleanup/v1", LayoutID: "layout", SourceSet: strings.Repeat("a", 64), TargetRevision: 1, SourceGeneration: 2, CutoffMillis: 123, Phase: "select", CountsExact: true}
			if err := WriteRecordingCleanupReceipt(t.Context(), owned.Directory, owned.Runtime, receipt); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(owned.Directory, ".recording-cleanup-123")
			if mode == "symlink" {
				if err := os.Symlink(filepath.Join(owned.Directory, RecordingCleanupReceiptName), path); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "oversized" {
					if err := os.Truncate(path, 64<<10+1); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := RetireRecordingCleanupArtifacts(t.Context(), owned.Directory, owned.Runtime, receipt); err == nil {
				t.Fatal("unsupported metadata artifact retired")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unowned artifact lost", err)
			}
		})
	}
}
