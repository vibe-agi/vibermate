package runtimeusage_test

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

func TestProjectorAttributesExactModelsTokensAndResumedAgentSession(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	users := fakeUsers{items: []runtimeuser.User{
		{ID: runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA"), Username: "alice", State: runtimeuser.StateActive, CreatedAt: now, UpdatedAt: now},
		{ID: runtimeuser.UserID("user.BBBBBBBBBBBBBBBBBBBBBBBBBBB"), Username: "bob", State: runtimeuser.StateDisabled, CreatedAt: now, UpdatedAt: now},
	}}
	runs := fakeRuns{items: []capturerun.View{
		{ID: "run-one", RuntimeUserID: users.items[0].ID, LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"), DeviceName: "Linux workstation", MachineID: "machine-one", WorkspaceID: "workspace-one", WorkspaceLabel: "repo", State: capturerun.StateFinished, UpdatedAt: now.Add(-time.Minute)},
		{ID: "run-two", RuntimeUserID: users.items[0].ID, LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"), DeviceName: "Linux workstation", MachineID: "machine-one", WorkspaceID: "workspace-one", WorkspaceLabel: "repo", State: capturerun.StateFinished, UpdatedAt: now},
	}}
	activities := fakeActivities{byRun: map[string][]activity.Record{
		"run-one": {
			{SubjectID: "exchange-one", Status: activity.StatusSucceeded, OccurredAt: now.Add(-3 * time.Minute)},
			{SubjectID: "exchange-two", Status: activity.StatusFailed, OccurredAt: now.Add(-2 * time.Minute)},
		},
		"run-two": {
			{SubjectID: "exchange-three", Status: activity.StatusSucceeded, OccurredAt: now.Add(-time.Minute)},
		},
	}}
	contents := fakeContents{items: map[string]exchangecontent.Record{
		"exchange-one": content("exchange-one", true),
		"exchange-two": content("exchange-two", false),
	}}
	identities := fakeIdentities{items: map[string]agentconversation.ClientIdentity{}}
	for _, exchangeID := range []string{"exchange-one", "exchange-two", "exchange-three"} {
		identities.items[exchangeID] = agentconversation.ClientIdentity{
			Client: "codex", SessionID: "01a02deb-d420-79e2-b0bc-1a9cbdaa643f",
			SessionResumable: true, Source: agentconversation.ClientIdentitySourceProtocolEvidence,
			Confidence: "exact", ObservedAt: now,
		}
	}
	projector, err := runtimeusage.New(runtimeusage.Options{
		Users: users, Runs: runs, Activities: activities, Contents: contents,
		Identities: identities, Clock: fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := projector.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Schema != runtimeusage.ReportSchema || report.GeneratedAt != now || report.Truncated {
		t.Fatalf("report header = %#v", report)
	}
	if len(report.Users) != 2 || report.Users[0].Username != "alice" || report.Users[1].Username != "bob" {
		t.Fatalf("users = %#v", report.Users)
	}
	alice := report.Users[0]
	if alice.CaptureRuns != 2 || alice.Turns != 3 || alice.Succeeded != 2 || alice.Failed != 1 ||
		alice.ContentUnavailableTurns != 1 || alice.ModelUnavailableTurns != 1 {
		t.Fatalf("alice counters = %#v", alice)
	}
	if len(alice.Models) != 1 || alice.Models[0].RequestedModel != "gpt-5.6-sol" ||
		alice.Models[0].UpstreamModel != "dashscope:deepseek-v4-flash-0731" ||
		alice.Models[0].Turns != 2 {
		t.Fatalf("model usage = %#v", alice.Models)
	}
	if alice.Tokens.InputUncached.Tokens != 10 || alice.Tokens.InputUncached.KnownTurns != 1 ||
		alice.Tokens.InputUncached.UnknownTurns != 2 {
		t.Fatalf("input token usage = %#v", alice.Tokens.InputUncached)
	}
	if len(alice.AgentSessions) != 1 || alice.AgentSessions[0].Client != "codex" ||
		alice.AgentSessions[0].SessionID != "01a02deb-d420-79e2-b0bc-1a9cbdaa643f" ||
		alice.AgentSessions[0].Turns != 3 || alice.AgentSessions[0].CaptureRuns != 2 {
		t.Fatalf("Agent Session usage = %#v", alice.AgentSessions)
	}
	if len(alice.Contexts) != 1 || alice.Contexts[0].DeviceName != "Linux workstation" ||
		alice.Contexts[0].WorkspaceLabel != "repo" || alice.Contexts[0].CaptureRuns != 2 ||
		alice.Contexts[0].Turns != 3 {
		t.Fatalf("context usage = %#v", alice.Contexts)
	}
	if report.Users[1].Turns != 0 || len(report.Users[1].Models) != 0 {
		t.Fatalf("zero-use Runtime User = %#v", report.Users[1])
	}
}

func content(exchangeID string, withResponse bool) exchangecontent.Record {
	record := exchangecontent.Record{
		ExchangeID: exchangeID,
		Request: exchangecontent.Request{
			RequestedModel: "gpt-5.6-sol", EffectiveModel: "dashscope:deepseek-v4-flash-0731",
		},
	}
	if withResponse {
		record.Response = &exchangecontent.Response{Usage: exchangecontent.Usage{
			InputUncached: exchangecontent.UsageValue{Known: true, Tokens: 10, Source: "provider"},
			Output:        exchangecontent.UsageValue{Known: true, Tokens: 4, Source: "provider"},
		}}
	}
	return record
}

type fakeUsers struct{ items []runtimeuser.User }

func (users fakeUsers) List(context.Context) ([]runtimeuser.User, error) {
	return append([]runtimeuser.User(nil), users.items...), nil
}

type fakeRuns struct{ items []capturerun.View }

func (runs fakeRuns) ListRuns(_ context.Context, request capturerun.PageRequest) (capturerun.Page, error) {
	if request.Cursor != nil {
		return capturerun.Page{}, nil
	}
	return capturerun.Page{Items: append([]capturerun.View(nil), runs.items...)}, nil
}

type fakeActivities struct{ byRun map[string][]activity.Record }

func (activities fakeActivities) ListExchanges(_ context.Context, request activity.PageRequest) (activity.Page, error) {
	if request.BeforeSequence != 0 {
		return activity.Page{}, nil
	}
	return activity.Page{Items: append([]activity.Record(nil), activities.byRun[request.CaptureRunID]...)}, nil
}

type fakeContents struct {
	items map[string]exchangecontent.Record
}

func (contents fakeContents) Get(_ context.Context, exchangeID string) (exchangecontent.Record, error) {
	record, ok := contents.items[exchangeID]
	if !ok {
		return exchangecontent.Record{}, exchangecontent.ErrNotFound
	}
	return record, nil
}

type fakeIdentities struct {
	items map[string]agentconversation.ClientIdentity
}

func (identities fakeIdentities) GetConversationIdentity(_ context.Context, exchangeID string) (agentconversation.ClientIdentity, error) {
	identity, ok := identities.items[exchangeID]
	if !ok {
		return agentconversation.ClientIdentity{}, activity.ErrExchangeNotFound
	}
	return identity, nil
}

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }
