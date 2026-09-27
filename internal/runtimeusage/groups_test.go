package runtimeusage_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

func TestUsageDrilldownTotalsAndCallerIsolation(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	user := runtimeuser.User{ID: "user.AAAAAAAAAAAAAAAAAAAAAAAAAAA", Username: "alice", State: runtimeuser.StateActive, CreatedAt: now, UpdatedAt: now}
	local := capturerun.View{LocalUserLabel: "alice", MachineID: "machine-one", Runtime: capturerun.RuntimeMetadata{GitAtLaunch: &capturerun.GitSnapshot{RepositoryKey: strings.Repeat("a", 64), RepositoryName: "project", Branch: "main"}}}
	member := local
	member.RuntimeUserID, member.RuntimeUsername = user.ID, user.Username
	member.Runtime.GitAtLaunch = &capturerun.GitSnapshot{RepositoryKey: strings.Repeat("a", 64), RepositoryName: "project", Branch: "feature"}
	ledger := &observationLedger{items: []runtimeusage.Observation{
		{ExchangeID: "local", Source: "local", OccurredAt: now, Status: activity.StatusSucceeded, EnvironmentID: "p", AccountID: "a", UpstreamModel: "model-a", Attribution: runtimeusage.CaptureAttribution(local)},
		{ExchangeID: "member", Source: "member", UserID: user.ID, OccurredAt: now, Status: activity.StatusFailed, EnvironmentID: "p", AccountID: "a", UpstreamModel: "model-b", Attribution: runtimeusage.CaptureAttribution(member)},
		{ExchangeID: "unknown", Source: "manual", OccurredAt: now, Status: activity.StatusCanceled},
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{Users: usersOf(user), Runs: fakeRuns{}, Ledger: ledger, Clock: fixedClock{now}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := projector.Report(context.Background(), queryAround(t, now))
	if err != nil {
		t.Fatal(err)
	}
	var check func(runtimeusage.GroupUsage)
	check = func(parent runtimeusage.GroupUsage) {
		if len(parent.Children) == 0 {
			return
		}
		calls, failures, unpriced := 0, 0, 0
		for _, child := range parent.Children {
			calls += child.AgentAPICalls
			failures += child.Failed
			unpriced += child.Cost.UnpricedCalls
			check(child)
		}
		if calls != parent.AgentAPICalls || failures != parent.Failed || unpriced != parent.Cost.UnpricedCalls {
			t.Fatalf("child totals disagree: %+v", parent)
		}
	}
	for _, groups := range [][]runtimeusage.GroupUsage{report.Sources, report.Profiles, report.Accounts, report.Models, report.Callers, report.Projects} {
		check(runtimeusage.GroupUsage{AgentAPICalls: report.Total.AgentAPICalls, Failed: report.Total.Failed, Cost: report.Total.Cost, Children: groups})
	}
	if len(report.Projects) != 2 || len(report.Projects[0].Children) != 2 || len(report.Callers) != 3 {
		t.Fatal("project/branch or unknown/caller identities were merged")
	}
	otherMachine := local
	otherMachine.MachineID = "machine-two"
	if runtimeusage.CaptureAttribution(otherMachine).ProjectID == ledger.items[0].Attribution.ProjectID || runtimeusage.CaptureAttribution(otherMachine).CallerID == ledger.items[0].Attribution.CallerID {
		t.Fatal("same labels merged unrelated machines")
	}
	self, err := projector.ReportForUser(context.Background(), queryAround(t, now), user.ID)
	if err != nil || self.Total.AgentAPICalls != 1 || len(self.Callers) != 1 || self.Callers[0].ID != string(user.ID) || len(self.Projects) != 1 || len(self.Projects[0].Children) != 1 || self.Projects[0].Children[0].Label != "feature" {
		t.Fatalf("nested member report leaked another caller: %+v %v", self.Projects, err)
	}
}

func TestUsageDrilldownBoundsNodesWithoutChangingTotal(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	ledger := &observationLedger{}
	for index := range 5100 {
		ledger.items = append(ledger.items, runtimeusage.Observation{ExchangeID: fmt.Sprint(index), Source: "local", OccurredAt: now, Status: activity.StatusSucceeded, EnvironmentID: "p", UpstreamModel: fmt.Sprint(index)})
	}
	projector, err := runtimeusage.New(runtimeusage.Options{Users: usersOf(), Runs: fakeRuns{}, Ledger: ledger, Clock: fixedClock{now}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := projector.Report(context.Background(), queryAround(t, now))
	if err != nil || !report.Truncated || report.Total.AgentAPICalls != 5100 || len(report.Profiles) != 1 || report.Profiles[0].AgentAPICalls != 5100 || !report.Profiles[0].ChildrenTruncated || len(report.Profiles[0].Children) != 4999 {
		t.Fatalf("unbounded or misleading nested report: total=%+v profiles=%d truncated=%v error=%v", report.Total, len(report.Profiles), report.Truncated, err)
	}
}
