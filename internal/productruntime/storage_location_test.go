package productruntime

import (
	"encoding/json"
	"github.com/vibe-agi/vibermate/internal/hostcontract"
	"path/filepath"
	"testing"
	"time"
)

func TestStorageCapacityStateUsesThePublishedThreshold(t *testing.T) {
	below := storageLowSpaceThresholdBytes - 1
	at := storageLowSpaceThresholdBytes
	if storageCapacityState(nil) != "unavailable" ||
		storageCapacityState(&below) != "low" ||
		storageCapacityState(&at) != "healthy" {
		t.Fatal("storage capacity threshold classification changed")
	}
}

func TestRuntimeStorageAdministrationUsesIndependentRolesAndDurableExpiry(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	location, err := r.StorageLocation(t.Context())
	if err != nil {
		t.Fatal("statistics still unavailable", err)
	}
	if location.DatabasePath != filepath.Join(r.paths.DataDirectory(), "runtime.db") || location.DatabaseBytes <= 0 || location.EvidenceBytes < 0 {
		t.Fatalf("invalid storage snapshot: %+v", location)
	}
	preview, err := r.EvidenceArchivePreview(t.Context())
	if err != nil || !preview.PrivacyMetadataRetained {
		t.Fatalf("archive preview unavailable/incomplete: %+v %v", preview, err)
	}
	receipt, err := r.CleanupExpiredStorage(t.Context())
	if err != nil || !receipt.CleanupPending || !receipt.CountsIncomplete {
		t.Fatalf("expiry did not accept durable pending work: %+v %v", receipt, err)
	}
	state, err := r.storage.RecordingCleanupState(t.Context())
	if err != nil || state.RequestedRevision == 0 {
		t.Fatalf("expiry lacks required intent: %+v %v", state, err)
	}
}

func TestRuntimeStorageLastCleanupUsesThePublicReceiptContract(t *testing.T) {
	r := startTestRuntime(t, testOptions(t, hostcontract.Desktop(), &coordinatorDouble{}))
	defer shutdownRuntime(t, r)
	waitRecording(t, r)
	if _, err := r.CleanupExpiredStorage(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		state, err := r.storage.RecordingCleanupState(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if state.CompletedRevision == state.RequestedRevision && state.ActiveRevision == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	location, err := r.StorageLocation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(location)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if err = json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err = json.Unmarshal(wire["lastCleanup"], &receipt); err != nil {
		t.Fatal("acknowledged certificate missing from storage", err)
	}
	if _, ok := receipt["countsIncomplete"]; !ok {
		t.Fatalf("last cleanup violates actual public receipt fields: %s", wire["lastCleanup"])
	}
}
