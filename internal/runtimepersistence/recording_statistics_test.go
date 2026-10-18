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

func TestRecordingStorageStatisticsPublishedRuntime3IncludesNativeApproval(t *testing.T) {
	f := newEgressInspectionFixture(t, 3)
	nativeExec(t, f.legacy, `INSERT INTO native_tool_approvals VALUES('cccccccccccccccccccccccccccccccc','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',zeroblob(32),1,'allowed',1,10,2,'native-decision-0001',3)`)
	_, preview, err := f.reader.catalog.InspectRecordingStorage(t.Context(), f.control, time.Now().UTC(), true)
	if err != nil || preview.Approvals != 1 {
		t.Fatalf("runtime3 native approval preview %+v %v", preview, err)
	}
}
