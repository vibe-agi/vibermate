package runtimepersistence

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
)

// SummarizeExchanges only needs status, failure reason, and occurrence time from
// lifecycle winners. Projecting the full activity row forces SQLite to carry
// transport JSON and conversation chrome through every GROUP BY input row.
func TestExchangeSummaryCandidateQueryIsThin(t *testing.T) {
	t.Parallel()
	query, args := exchangeSummaryCandidateQuery(activity.PageRequest{
		CaptureRunID: "run-summary-thin",
	})
	if len(args) != 1 || args[0] != "run-summary-thin" {
		t.Fatalf("capture filter args: %#v", args)
	}
	for _, required := range []string{
		"status",
		"reason_code",
		"occurred_at_unix_ms",
		"subject_id",
		"kind",
		"capture_run_id",
		"runtime_activities AS candidate",
		"NOT EXISTS",
	} {
		if !strings.Contains(query, required) {
			t.Fatalf("summary candidate query missing %q:\n%s", required, query)
		}
	}
	for _, forbidden := range []string{
		"transport_evidence_json",
		"conversation_display_name",
		"conversation_projection_id",
		"environment_digest",
		"client_endpoint_id",
		"protocol_plan_id",
		"account_id",
		"source_display_name",
		"provider_status",
		"client_path",
	} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("summary candidate query must not project %s:\n%s", forbidden, query)
		}
	}
	pageQuery, _ := exchangeCandidateQuery(activity.PageRequest{
		CaptureRunID: "run-summary-thin",
	})
	if !strings.Contains(pageQuery, "transport_evidence_json") {
		t.Fatal("page candidate query must keep the full projection")
	}
}
