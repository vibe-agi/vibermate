package runtimeusage

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"github.com/vibe-agi/vibermate/internal/capturerun"
)

// CaptureAttribution copies only display metadata from the already admitted
// Capture. Local names are launcher-reported, never authentication evidence.
func CaptureAttribution(run capturerun.View) *Attribution {
	value := &Attribution{}
	if run.RuntimeUserID != "" {
		value.CallerID, value.CallerLabel, value.CallerKind = string(run.RuntimeUserID), run.RuntimeUsername, "member"
	} else if run.LocalUserLabel != "" {
		value.CallerID, value.CallerLabel, value.CallerKind = scopedUsageID("local", run.MachineID, run.LocalUserLabel), run.LocalUserLabel, "local"
	}
	if git := run.Runtime.GitAtLaunch; git != nil {
		cloned := *git
		value.GitAtLaunch = &cloned
		value.ProjectID = scopedUsageID("git", run.MachineID, git.RepositoryKey)
	}
	return value
}

func scopedUsageID(kind, machine, value string) string {
	digest := sha256.Sum256([]byte(machine + "\x00" + value))
	return kind + ":" + hex.EncodeToString(digest[:])
}

type groupLabel struct{ dimension, id, label, evidence string }
type groupAccumulator struct {
	view     GroupUsage
	children map[string]*groupAccumulator
}

func addGroup(groups map[string]*groupAccumulator, path []groupLabel, record Observation, cost CostEstimate, budget *int, truncated *bool) {
	var parent *groupAccumulator
	for _, label := range path {
		group := groups[label.id]
		if group == nil {
			if *budget == 0 {
				*truncated = true
				if parent != nil {
					parent.view.ChildrenTruncated = true
				}
				return
			}
			*budget--
			group = &groupAccumulator{view: GroupUsage{ID: label.id, Label: label.label, Dimension: label.dimension, Evidence: label.evidence}, children: map[string]*groupAccumulator{}}
			groups[label.id] = group
		}
		group.view.add(record)
		group.view.Cost.add(cost)
		groups = group.children
		parent = group
	}
}

func finishGroups(groups map[string]*groupAccumulator) []GroupUsage {
	ordered := make([]*groupAccumulator, 0, len(groups))
	for _, group := range groups {
		ordered = append(ordered, group)
	}
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i].view, ordered[j].view
		if left.AgentAPICalls != right.AgentAPICalls {
			return left.AgentAPICalls > right.AgentAPICalls
		}
		return left.ID < right.ID
	})
	result := make([]GroupUsage, 0, len(ordered))
	for _, group := range ordered {
		group.view.Children = finishGroups(group.children)
		result = append(result, group.view)
	}
	return result
}
