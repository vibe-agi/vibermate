package activity

import (
	"fmt"
	"time"
)

// ScriptActivity says what message scripts did to recent requests. It counts
// requests, not outcome rows: a script that ran on both the request and the
// response of one request counts once, with its most severe outcome.
//
// It covers recorded, completed requests that are still retained. When the
// newest requests already exceed the read bound, CoveredFrom is later than
// Since, and the screen says so instead of claiming the whole window.
type ScriptActivity struct {
	Since       time.Time     `json:"since"`
	CoveredFrom time.Time     `json:"coveredFrom"`
	GeneratedAt time.Time     `json:"generatedAt"`
	Requests    int           `json:"requests"`
	Truncated   bool          `json:"truncated"`
	Scripts     []ScriptTally `json:"scripts"`
}

// ScriptTally is one script's share of ScriptActivity.
type ScriptTally struct {
	ScriptID     string             `json:"scriptId"`
	Name         string             `json:"name,omitempty"`
	Revision     uint64             `json:"revision,omitempty"`
	Requests     int                `json:"requests"`
	Changed      int                `json:"changed"`
	Blocked      int                `json:"blocked"`
	Failed       int                `json:"failed"`
	BlockReasons []BlockReasonCount `json:"blockReasons"`
}

// BlockReasonCount is how often one reason refused a request.
type BlockReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

const (
	// MaximumScriptActivityDays is the longest window that can be asked for.
	MaximumScriptActivityDays = 90
	// MaximumScriptActivityRequests bounds how many of the newest requests one
	// read examines, so the read stays cheap however much history is kept.
	MaximumScriptActivityRequests = 20000
	// MaximumScriptBlockReasons is how many distinct reasons each script lists.
	MaximumScriptBlockReasons = 5
)

// ScriptActivityQuery asks for the scripts' activity since a time.
type ScriptActivityQuery struct {
	Since time.Time
}

func (query ScriptActivityQuery) Validate(now time.Time) error {
	if query.Since.IsZero() || query.Since.After(now) ||
		now.Sub(query.Since) > MaximumScriptActivityDays*24*time.Hour {
		return fmt.Errorf("%w: script activity window is out of range", ErrInvalidEvent)
	}
	return nil
}

// TallyScriptOutcomes adds one completed request's outcomes to tallies keyed
// by script id. A script appears once per request, with its most severe
// outcome: blocked, then failed, then changed, then unchanged.
func TallyScriptOutcomes(tallies map[string]*ScriptTally, outcomes []ScriptOutcome) {
	type worst struct {
		outcome  string
		reason   string
		name     string
		revision uint64
	}
	severity := map[string]int{ScriptUnchanged: 0, ScriptChanged: 1, ScriptFailed: 2, ScriptBlocked: 3}
	perScript := map[string]worst{}
	for _, outcome := range outcomes {
		if outcome.ScriptID == "" {
			continue
		}
		current, seen := perScript[outcome.ScriptID]
		if !seen || severity[outcome.Outcome] > severity[current.outcome] {
			current.outcome, current.reason = outcome.Outcome, outcome.BlockReason
		}
		if outcome.Name != "" {
			current.name = outcome.Name
		}
		if outcome.Revision > current.revision {
			current.revision = outcome.Revision
		}
		perScript[outcome.ScriptID] = current
	}
	for id, result := range perScript {
		tally := tallies[id]
		if tally == nil {
			tally = &ScriptTally{ScriptID: id, BlockReasons: []BlockReasonCount{}}
			tallies[id] = tally
		}
		if result.name != "" {
			tally.Name = result.name
		}
		if result.revision > tally.Revision {
			tally.Revision = result.revision
		}
		tally.Requests++
		switch result.outcome {
		case ScriptChanged:
			tally.Changed++
		case ScriptFailed:
			tally.Failed++
		case ScriptBlocked:
			tally.Blocked++
			countBlockReason(tally, result.reason)
		}
	}
}

func countBlockReason(tally *ScriptTally, reason string) {
	if reason == "" {
		return
	}
	for index := range tally.BlockReasons {
		if tally.BlockReasons[index].Reason == reason {
			tally.BlockReasons[index].Count++
			return
		}
	}
	tally.BlockReasons = append(tally.BlockReasons, BlockReasonCount{Reason: reason, Count: 1})
}
