package runtimeusage_test

import (
	"context"
	"fmt"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

func TestProjectorContinuesAfterAFullCaptureRunPage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	user := runtimeuser.User{
		ID:       runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		Username: "alice", State: runtimeuser.StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	items := make([]capturerun.View, capturerun.MaxPageLimit+1)
	for index := range items {
		items[index] = capturerun.View{
			ID: fmt.Sprintf("run-%03d", index), RuntimeUserID: user.ID,
			LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
			DeviceName:     "device", MachineID: "machine", State: capturerun.StateFinished,
			UpdatedAt: now.Add(-time.Duration(index) * time.Millisecond),
		}
	}
	runs := &validatingPagedRuns{items: items}
	projector, err := runtimeusage.New(runtimeusage.Options{
		Users: fakeUsers{items: []runtimeuser.User{user}}, Runs: runs,
		Activities: fakeActivities{byRun: map[string][]activity.Record{}},
		Contents:   fakeContents{items: map[string]exchangecontent.Record{}},
		Identities: fakeIdentities{items: map[string]agentconversation.ClientIdentity{}},
		Clock:      fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := projector.Report(context.Background(), queryAround(t, now))
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if report.Users[0].CaptureRuns != len(items) || len(runs.requests) != 2 {
		t.Fatalf("report=%#v requests=%#v", report.Users[0], runs.requests)
	}
	if cursor := runs.requests[1].Cursor; cursor == nil || !cursor.Valid() {
		t.Fatalf("second page cursor = %#v", cursor)
	}
}

func TestProjectorKeepsAnOverflowingTokenTurnUnknown(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	user := runtimeuser.User{
		ID:       runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		Username: "alice", State: runtimeuser.StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	run := capturerun.View{
		ID: "run-one", RuntimeUserID: user.ID,
		LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		DeviceName:     "device", MachineID: "machine", State: capturerun.StateFinished,
		UpdatedAt: now,
	}
	contents := fakeContents{items: map[string]exchangecontent.Record{
		"exchange-one": usageContent("exchange-one", math.MaxInt64),
		"exchange-two": usageContent("exchange-two", 1),
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{
		Users: fakeUsers{items: []runtimeuser.User{user}},
		Runs:  fakeRuns{items: []capturerun.View{run}},
		Activities: fakeActivities{byRun: map[string][]activity.Record{
			"run-one": {
				{SubjectID: "exchange-one", Status: activity.StatusSucceeded, OccurredAt: now},
				{SubjectID: "exchange-two", Status: activity.StatusSucceeded, OccurredAt: now},
			},
		}},
		Contents:   contents,
		Identities: fakeIdentities{items: map[string]agentconversation.ClientIdentity{}},
		Clock:      fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := projector.Report(context.Background(), queryAround(t, now))
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	aggregate := report.Users[0].Tokens.Output
	if aggregate.Tokens != math.MaxInt64 || aggregate.KnownTurns != 1 ||
		aggregate.UnknownTurns != 1 {
		t.Fatalf("overflow aggregate = %#v", aggregate)
	}
}

func TestProjectorReportsSparseDaysWithinAnExplicitCivilWindow(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 3, 4, 5, 0, time.UTC)
	user := runtimeuser.User{
		ID:       runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		Username: "alice", State: runtimeuser.StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	run := capturerun.View{
		ID: "run-one", RuntimeUserID: user.ID,
		LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		DeviceName:     "device", MachineID: "machine", WorkspaceID: "workspace-one",
		WorkspaceLabel: "repo", State: capturerun.StateFinished,
		CreatedAt: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), UpdatedAt: now,
	}
	activities := fakeActivities{byRun: map[string][]activity.Record{
		"run-one": {
			{SubjectID: "before", Status: activity.StatusSucceeded, OccurredAt: time.Date(2026, 8, 23, 15, 59, 59, 0, time.UTC)},
			{SubjectID: "day-one", Status: activity.StatusSucceeded, OccurredAt: time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)},
			{SubjectID: "day-two", Status: activity.StatusFailed, OccurredAt: time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)},
			{SubjectID: "until", Status: activity.StatusSucceeded, OccurredAt: time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)},
		},
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{
		Users: usersOf(user), Runs: fakeRuns{items: []capturerun.View{run}},
		Activities: activities,
		Contents: fakeContents{items: map[string]exchangecontent.Record{
			"day-one": usageContent("day-one", 7),
		}},
		Identities: fakeIdentities{items: map[string]agentconversation.ClientIdentity{}},
		Clock:      fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := runtimeusage.NewQuery(
		"2026-08-24", "2026-08-27", "Asia/Singapore",
	)
	if err != nil {
		t.Fatal(err)
	}

	report, err := projector.Report(context.Background(), query)
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if report.Period.From != "2026-08-24" || report.Period.Until != "2026-08-27" ||
		report.Period.TimeZone != "Asia/Singapore" {
		t.Fatalf("period = %#v", report.Period)
	}
	if report.Users[0].Turns != 2 || report.Users[0].CaptureRuns != 1 ||
		report.Users[0].Succeeded != 1 || report.Users[0].Failed != 1 {
		t.Fatalf("windowed totals = %#v", report.Users[0])
	}
	wantDates := []string{"2026-08-24", "2026-08-25"}
	if got := usageDates(report.Days); !slices.Equal(got, wantDates) {
		t.Fatalf("team day dates = %v, want %v", got, wantDates)
	}
	if got := usageDates(report.Users[0].Days); !slices.Equal(got, wantDates) {
		t.Fatalf("user day dates = %v, want %v", got, wantDates)
	}
	if report.Days[0].Turns != 1 || report.Days[0].Tokens.Output.Tokens != 7 ||
		report.Days[0].Tokens.Output.KnownTurns != 1 ||
		report.Days[1].Failed != 1 || report.Days[1].ContentUnavailableTurns != 1 ||
		report.Days[1].Tokens.Output.UnknownTurns != 1 {
		t.Fatalf("daily evidence = %#v", report.Days)
	}
}

func TestProjectorReadsOneWindowedExchangeStreamForAllCaptureRuns(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	user := runtimeuser.User{
		ID:       runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		Username: "alice", State: runtimeuser.StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	runs := []capturerun.View{
		{
			ID: "run-one", RuntimeUserID: user.ID,
			LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
			DeviceName:     "one", MachineID: "machine-one",
			State: capturerun.StateFinished, UpdatedAt: now,
		},
		{
			ID: "run-two", RuntimeUserID: user.ID,
			LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
			DeviceName:     "two", MachineID: "machine-two",
			State: capturerun.StateFinished, UpdatedAt: now,
		},
	}
	activities := &recordingActivities{items: []activity.Record{
		{
			SubjectID: "exchange-one", CaptureRunID: "run-one",
			Status: activity.StatusSucceeded, OccurredAt: now,
		},
		{
			SubjectID: "exchange-two", CaptureRunID: "run-two",
			Status: activity.StatusSucceeded, OccurredAt: now,
		},
	}}
	projector, err := runtimeusage.New(runtimeusage.Options{
		Users: usersOf(user), Runs: fakeRuns{items: runs}, Activities: activities,
		Contents:   fakeContents{items: map[string]exchangecontent.Record{}},
		Identities: fakeIdentities{items: map[string]agentconversation.ClientIdentity{}},
		Clock:      fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	query, err := runtimeusage.NewQuery("2026-08-24", "2026-08-26", "UTC")
	if err != nil {
		t.Fatal(err)
	}

	report, err := projector.Report(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities.requests) != 1 {
		t.Fatalf("ListExchanges requests = %#v", activities.requests)
	}
	request := activities.requests[0]
	if request.CaptureRunID != "" ||
		request.OccurredAtOrAfter != time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) ||
		request.OccurredBefore != time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC) {
		t.Fatalf("windowed request = %#v", request)
	}
	if report.Users[0].Turns != 2 || report.Users[0].CaptureRuns != 2 {
		t.Fatalf("cross-run report = %#v", report.Users[0])
	}
}

func usersOf(users ...runtimeuser.User) fakeUsers { return fakeUsers{items: users} }

func usageDates(days []runtimeusage.DayUsage) []string {
	dates := make([]string, len(days))
	for index, day := range days {
		dates[index] = day.Date
	}
	return dates
}

func TestProjectorBoundsBreakdownsWithoutChangingUserTotals(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 1, 2, 3, 0, time.UTC)
	user := runtimeuser.User{
		ID:       runtimeuser.UserID("user.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		Username: "alice", State: runtimeuser.StateActive,
		CreatedAt: now, UpdatedAt: now,
	}
	run := capturerun.View{
		ID: "run-one", RuntimeUserID: user.ID,
		LoginSessionID: runtimeuser.LoginSessionID("login.AAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		DeviceName:     "device", MachineID: "machine", State: capturerun.StateFinished,
		UpdatedAt: now,
	}
	records := make([]activity.Record, 51)
	contents := make(map[string]exchangecontent.Record, len(records))
	for index := range records {
		exchangeID := fmt.Sprintf("exchange-%02d", index)
		records[index] = activity.Record{
			SubjectID: exchangeID, Status: activity.StatusSucceeded,
			OccurredAt: now.Add(time.Duration(index) * time.Second),
		}
		contents[exchangeID] = exchangecontent.Record{
			ExchangeID: exchangeID,
			Request: exchangecontent.Request{
				RequestedModel: fmt.Sprintf("requested:%02d", index),
				EffectiveModel: fmt.Sprintf("upstream:%02d", index),
			},
		}
	}
	projector, err := runtimeusage.New(runtimeusage.Options{
		Users: fakeUsers{items: []runtimeuser.User{user}},
		Runs:  fakeRuns{items: []capturerun.View{run}},
		Activities: fakeActivities{byRun: map[string][]activity.Record{
			"run-one": records,
		}},
		Contents:   fakeContents{items: contents},
		Identities: fakeIdentities{items: map[string]agentconversation.ClientIdentity{}},
		Clock:      fixedClock{now: now},
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := projector.Report(context.Background(), queryAround(t, now))
	if err != nil {
		t.Fatalf("Report() error = %v", err)
	}
	if report.Users[0].Turns != 51 || len(report.Users[0].Models) != 50 ||
		!report.Truncated {
		t.Fatalf("bounded report = %#v", report)
	}
}

func usageContent(exchangeID string, tokens int64) exchangecontent.Record {
	return exchangecontent.Record{
		ExchangeID: exchangeID,
		Request: exchangecontent.Request{
			RequestedModel: "requested", EffectiveModel: "upstream",
		},
		Response: &exchangecontent.Response{Usage: exchangecontent.Usage{
			Output: exchangecontent.UsageValue{Known: true, Tokens: tokens, Source: "provider"},
		}},
	}
}

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

	report, err := projector.Report(context.Background(), queryAround(t, now))
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

type validatingPagedRuns struct {
	items    []capturerun.View
	requests []capturerun.PageRequest
}

func (runs *validatingPagedRuns) ListRuns(
	_ context.Context,
	request capturerun.PageRequest,
) (capturerun.Page, error) {
	if request.Cursor != nil && !request.Cursor.Valid() {
		return capturerun.Page{}, capturerun.ErrInvalidRequest
	}
	runs.requests = append(runs.requests, request)
	start := len(runs.requests) - 1
	start *= capturerun.MaxPageLimit
	if start >= len(runs.items) {
		return capturerun.Page{}, nil
	}
	end := min(start+capturerun.MaxPageLimit, len(runs.items))
	return capturerun.Page{Items: append([]capturerun.View(nil), runs.items[start:end]...)}, nil
}

type fakeActivities struct{ byRun map[string][]activity.Record }

func (activities fakeActivities) ListExchanges(_ context.Context, request activity.PageRequest) (activity.Page, error) {
	if request.BeforeSequence != 0 {
		return activity.Page{}, nil
	}
	if request.CaptureRunID != "" {
		return activity.Page{Items: append([]activity.Record(nil), activities.byRun[request.CaptureRunID]...)}, nil
	}
	items := []activity.Record{}
	for runID, records := range activities.byRun {
		for _, source := range records {
			record := source
			if record.CaptureRunID == "" {
				record.CaptureRunID = runID
			}
			if (!request.OccurredAtOrAfter.IsZero() &&
				record.OccurredAt.Before(request.OccurredAtOrAfter)) ||
				(!request.OccurredBefore.IsZero() &&
					!record.OccurredAt.Before(request.OccurredBefore)) {
				continue
			}
			items = append(items, record)
		}
	}
	return activity.Page{Items: items}, nil
}

type recordingActivities struct {
	items    []activity.Record
	requests []activity.PageRequest
}

func (activities *recordingActivities) ListExchanges(
	_ context.Context,
	request activity.PageRequest,
) (activity.Page, error) {
	activities.requests = append(activities.requests, request)
	if request.BeforeSequence != 0 {
		return activity.Page{}, nil
	}
	return activity.Page{Items: append([]activity.Record(nil), activities.items...)}, nil
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

func queryAround(t *testing.T, now time.Time) runtimeusage.Query {
	t.Helper()
	query, err := runtimeusage.NewQuery(
		now.AddDate(0, 0, -30).Format(time.DateOnly),
		now.AddDate(0, 0, 1).Format(time.DateOnly),
		"UTC",
	)
	if err != nil {
		t.Fatal(err)
	}
	return query
}
