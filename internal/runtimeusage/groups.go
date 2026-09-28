package runtimeusage

import (
	"crypto/sha256"
	"encoding/hex"

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
		if git.RepositorySource == "remote" {
			value.ProjectID = "git.remote:" + git.RepositoryKey
		} else {
			value.ProjectID = scopedUsageID("git.local", run.MachineID, git.RepositoryKey)
		}
	}
	return value
}

func scopedUsageID(kind, machine, value string) string {
	digest := sha256.Sum256([]byte(machine + "\x00" + value))
	return kind + ":" + hex.EncodeToString(digest[:])
}
