package runtimeusage_test

import (
	"strings"
	"testing"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
)

func TestProjectAndCallerIdentitiesUseFrozenLaunchContext(t *testing.T) {
	local := capturerun.View{LocalUserLabel: "alice", MachineID: "machine-one", Runtime: capturerun.RuntimeMetadata{GitAtLaunch: &capturerun.GitSnapshot{RepositorySource: "local", RepositoryKey: strings.Repeat("a", 64), RepositoryName: "project", Branch: "main"}}}
	first := runtimeusage.CaptureAttribution(local)
	otherMachine := local
	otherMachine.MachineID = "machine-two"
	second := runtimeusage.CaptureAttribution(otherMachine)
	if first.ProjectID == second.ProjectID || first.CallerID == second.CallerID {
		t.Fatal("same labels merged different local machines")
	}
	remote := local
	remote.Runtime = local.Runtime.Clone()
	remote.Runtime.GitAtLaunch.RepositorySource = "remote"
	remote.Runtime.GitAtLaunch.RepositoryName = "github.com/team/project"
	anotherClone := remote
	anotherClone.MachineID = "another-device"
	first, second = runtimeusage.CaptureAttribution(remote), runtimeusage.CaptureAttribution(anotherClone)
	if first.ProjectID != second.ProjectID || first.CallerID == second.CallerID {
		t.Fatal("remote clones did not merge independently of callers")
	}
	remote.Runtime.GitAtLaunch.Branch = "later"
	if first.GitAtLaunch.Branch != "main" {
		t.Fatal("mutable run changed frozen attribution")
	}
	member := local
	member.RuntimeUserID, member.RuntimeUsername = "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA", "alice"
	if runtimeusage.CaptureAttribution(member).CallerID == runtimeusage.CaptureAttribution(local).CallerID {
		t.Fatal("local label became authenticated identity")
	}
}
