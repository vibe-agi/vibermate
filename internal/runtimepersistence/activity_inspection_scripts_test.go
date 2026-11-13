package runtimepersistence

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
)

func TestScriptActivityCountsRequestsInTheWindow(t *testing.T) {
	f := newEgressInspectionFixture(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	// An archive from before script outcomes existed is read and counted as a
	// request, with nothing to tally.
	seedFrozenV1Activity(t, &Store{database: f.legacy}, inspectionActivity("legacy", "legacy-only", activity.KindExchangeCompleted, now.Add(-time.Hour)))
	put := func(id string, at time.Time, scripts ...activity.ScriptOutcome) {
		t.Helper()
		record := inspectionActivity(id, id+"-exchange", activity.KindExchangeCompleted, at)
		record.Scripts = scripts
		if _, err := f.current.ActivityRepository().(activity.ScopedRepository).AppendScoped(ctx, f.scope, record); err != nil {
			t.Fatal(err)
		}
	}
	mask := func(stage, outcome string) activity.ScriptOutcome {
		return activity.ScriptOutcome{ScriptID: "transform.mask", Revision: 3, Name: "Mask secrets", Stage: stage, Outcome: outcome, Applications: 1, BytesBefore: 10, BytesAfter: 10}
	}
	guard := activity.ScriptOutcome{ScriptID: "transform.guard", Revision: 1, Name: "Guard", Stage: activity.ScriptStageRequest, Outcome: activity.ScriptBlocked, BlockReason: "secret-in-prompt", Applications: 1, BytesBefore: 10, BytesAfter: 10}
	changed := mask(activity.ScriptStageRequest, activity.ScriptChanged)
	changed.Changed, changed.BodyChanged, changed.BytesAfter = 1, true, 8

	// Changed on the request and untouched on the response: one changed request.
	put("both-stages", now.Add(-2*time.Hour), changed, mask(activity.ScriptStageResponse, activity.ScriptUnchanged))
	put("blocked", now.Add(-3*time.Hour), mask(activity.ScriptStageRequest, activity.ScriptUnchanged), guard)
	put("untouched", now.Add(-4*time.Hour))
	// Outside the window.
	put("old", now.Add(-10*24*time.Hour), changed)

	reader := newActivityInspectionTestReader(t, f)
	got, err := reader.SummarizeScripts(ctx, activity.ScriptActivityQuery{Since: now.Add(-7 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Truncated || !got.CoveredFrom.Equal(got.Since) {
		t.Fatalf("a small history must cover the whole window: %+v", got)
	}
	if got.Requests != 4 {
		t.Fatalf("requests in the window = %d, want 4 (three current, one archived)", got.Requests)
	}
	if len(got.Scripts) != 2 {
		t.Fatalf("scripts = %+v", got.Scripts)
	}
	guardTally, maskTally := got.Scripts[0], got.Scripts[1]
	if maskTally.ScriptID != "transform.mask" || maskTally.Requests != 2 || maskTally.Changed != 1 || maskTally.Blocked != 0 || maskTally.Name != "Mask secrets" || maskTally.Revision != 3 {
		t.Fatalf("mask tally = %+v", maskTally)
	}
	if guardTally.ScriptID != "transform.guard" || guardTally.Requests != 1 || guardTally.Blocked != 1 ||
		len(guardTally.BlockReasons) != 1 || guardTally.BlockReasons[0] != (activity.BlockReasonCount{Reason: "secret-in-prompt", Count: 1}) {
		t.Fatalf("guard tally = %+v", guardTally)
	}

	if _, err := reader.SummarizeScripts(ctx, activity.ScriptActivityQuery{Since: now.Add(-91 * 24 * time.Hour)}); err == nil {
		t.Fatal("a window longer than 90 days was accepted")
	}
}
