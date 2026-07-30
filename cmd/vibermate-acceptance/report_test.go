package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReportPublishesPrivateRedactedEvidence(t *testing.T) {
	t.Parallel()

	report := newReport(time.Unix(10, 0))
	report.Provenance = &acceptanceProvenance{
		Source: sourceProvenance{
			VCS:      "git",
			Revision: "0123456789012345678901234567890123456789",
		},
	}
	report.add("deterministic-check", checkPassed, "bounded evidence")
	report.FinishedAt = time.Unix(20, 0).UTC()
	path := filepath.Join(t.TempDir(), "nested", "acceptance.json")
	if err := writeReport(path, report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if permission := info.Mode().Perm(); permission != 0o600 {
		t.Fatalf("report permission = %04o", permission)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded acceptanceReport
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Schema != reportSchema ||
		decoded.Status != checkPassed ||
		decoded.Provenance == nil ||
		decoded.Provenance.Source.Revision !=
			"0123456789012345678901234567890123456789" ||
		len(decoded.Checks) != 1 {
		t.Fatalf("decoded report = %+v", decoded)
	}
}
