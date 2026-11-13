package productruntime

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
)

func TestActivityScriptOutcomesKeepsWhatCanBeStored(t *testing.T) {
	ok := messagetransform.StageOutcome{Script: messagetransform.ScriptRef{ID: "transform.mask", Revision: 2, Name: "Mask"}, Stage: messagetransform.StageRequest, Outcome: messagetransform.OutcomeChanged, Applications: 1, Changed: 1, BodyChanged: true}
	unnamed := ok
	unnamed.Script = messagetransform.ScriptRef{}
	oversized := ok
	oversized.Script.Name = strings.Repeat("n", 300)
	got := activityScriptOutcomes([]messagetransform.StageOutcome{ok, oversized, unnamed})
	if len(got) != 2 || got[0].ScriptID != "transform.mask" || got[0].Outcome != activity.ScriptChanged || got[1].ScriptID != "" {
		t.Fatalf("one unusable outcome cost the others: %+v", got)
	}
	if activityScriptOutcomes(nil) != nil || activityScriptOutcomes([]messagetransform.StageOutcome{oversized}) != nil {
		t.Fatal("nothing storable must be no outcomes at all")
	}
	// With more scripts than one record holds, a block is not the one cut.
	quiet := ok
	quiet.Outcome, quiet.Changed, quiet.BodyChanged = messagetransform.OutcomeUnchanged, 0, false
	many := make([]messagetransform.StageOutcome, 0, activity.MaximumScriptOutcomes+5)
	for len(many) < activity.MaximumScriptOutcomes+4 {
		many = append(many, quiet)
	}
	blocked := ok
	blocked.Outcome, blocked.BlockReason, blocked.Changed, blocked.BodyChanged = messagetransform.OutcomeBlocked, "policy", 0, false
	many = append(many, blocked)
	got = activityScriptOutcomes(many)
	if len(got) != activity.MaximumScriptOutcomes || got[0].Outcome != activity.ScriptBlocked || got[0].BlockReason != "policy" {
		t.Fatalf("overflow dropped the block: %d %+v", len(got), got[0])
	}
	if activity.ValidateScriptOutcomes(got) != nil {
		t.Fatal("converted outcomes are not storable")
	}
}
