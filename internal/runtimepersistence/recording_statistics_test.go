package runtimepersistence

import (
	"testing"
	"time"
)

func TestRecordingStorageStatisticsCountEveryPhysicalSourceCopy(t *testing.T) {
	f := newEgressInspectionFixture(t)
	value := providerAttempt(t, "physical-copy")
	f.put(t, false, value)
	f.put(t, true, value)
	stats, preview, err := f.reader.catalog.InspectRecordingStorage(t.Context(), f.control, time.Now().UTC(), true)
	if err != nil || preview.Attempts != 2 || stats.EvidenceBytes <= 0 || !preview.PrivacyMetadataRetained {
		t.Fatalf("all-source snapshot: %+v %+v %v", stats, preview, err)
	}
	stats, _, err = f.reader.catalog.InspectRecordingStorage(t.Context(), f.control, time.Now().UTC(), false)
	if err != nil || !stats.Expired.CountsIncomplete {
		t.Fatalf("limited direct-expiry preview called complete: %+v %v", stats, err)
	}
}
