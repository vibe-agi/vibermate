package activity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The app parses api/samples/script-activity.json, so a change to what the
// Runtime sends fails the app's test instead of quietly hiding the counts.
func TestScriptActivitySampleDescribesWhatTheRuntimeSends(t *testing.T) {
	since := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	tallies := map[string]*ScriptTally{}
	mask := func(stage, outcome string) ScriptOutcome {
		return ScriptOutcome{ScriptID: "transform.mask", Revision: 4, Name: "Mask secrets", Stage: stage, Outcome: outcome, Applications: 1}
	}
	TallyScriptOutcomes(tallies, []ScriptOutcome{mask(ScriptStageRequest, ScriptChanged), mask(ScriptStageResponse, ScriptUnchanged)})
	TallyScriptOutcomes(tallies, []ScriptOutcome{mask(ScriptStageRequest, ScriptUnchanged), {ScriptID: "transform.guard", Revision: 2, Name: "Block dangerous commands", Stage: ScriptStageRequest, Outcome: ScriptBlocked, BlockReason: "destructive-command", Applications: 1}})
	TallyScriptOutcomes(tallies, []ScriptOutcome{{ScriptID: "transform.guard", Revision: 2, Stage: ScriptStageRequest, Outcome: ScriptFailed, Applications: 1}})
	sample := ScriptActivity{Since: since, CoveredFrom: since, GeneratedAt: since.Add(7 * 24 * time.Hour), Requests: 41, Scripts: []ScriptTally{*tallies["transform.guard"], *tallies["transform.mask"]}}
	if sample.Scripts[0].Requests != 2 || sample.Scripts[0].Blocked != 1 || sample.Scripts[0].Failed != 1 ||
		sample.Scripts[1].Requests != 2 || sample.Scripts[1].Changed != 1 {
		t.Fatalf("a script must count once per request with its most severe outcome: %+v", sample.Scripts)
	}
	encoded, err := json.MarshalIndent(sample, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "samples", "script-activity.json")
	if os.Getenv("VIBERMATE_UPDATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read script activity sample: %v", err)
	}
	if string(current) != string(encoded) {
		t.Fatal("the script activity sample is stale; rerun with VIBERMATE_UPDATE=1")
	}
}
