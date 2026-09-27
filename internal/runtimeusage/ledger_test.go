package runtimeusage_test

import (
	"context"
	"testing"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeusage"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

// Retain the existing aggregation fixtures, but materialize their observations
// at the ledger boundary. The production projector has no content/identity reader.
type testOptions struct {
	Users      runtimeusage.UserReader
	Runs       runtimeusage.CaptureRunReader
	Activities interface {
		ListExchanges(context.Context, activity.PageRequest) (activity.Page, error)
	}
	Contents   fakeContents
	Identities fakeIdentities
	Clock      fixedClock
}

func newTestProjector(options testOptions) (*runtimeusage.Projector, error) {
	return runtimeusage.New(runtimeusage.Options{Users: options.Users, Runs: options.Runs, Ledger: fixtureLedger{options}, Clock: options.Clock})
}

type fixtureLedger struct{ options testOptions }

func (fixtureLedger) RecordUsage(context.Context, runtimeusage.Observation) error { return nil }
func (fixtureLedger) UsagePolicy(context.Context) (runtimeusage.CollectionPolicy, error) {
	return runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, nil
}
func (fixtureLedger) SetUsagePolicy(_ context.Context, p runtimeusage.CollectionPolicy, _ time.Time) (runtimeusage.CollectionPolicy, error) {
	return p, nil
}
func (ledger fixtureLedger) ListUsage(ctx context.Context, query runtimeusage.Query, user runtimeuser.UserID, _ time.Time, _ int) ([]runtimeusage.Observation, bool, error) {
	from, until := query.Bounds()
	page, err := ledger.options.Activities.ListExchanges(ctx, activity.PageRequest{OccurredAtOrAfter: from, OccurredBefore: until, Limit: activity.MaxPageSize})
	if err != nil {
		return nil, false, err
	}
	var runs []capturerun.View
	switch source := ledger.options.Runs.(type) {
	case fakeRuns:
		runs = source.items
	case *validatingPagedRuns:
		runs = source.items
	}
	byRun := map[string]capturerun.View{}
	for _, run := range runs {
		byRun[run.ID] = run
	}
	result := []runtimeusage.Observation{}
	for _, event := range page.Items {
		run := byRun[event.CaptureRunID]
		if user != "" && run.RuntimeUserID != user {
			continue
		}
		value := runtimeusage.Observation{ExchangeID: event.SubjectID, CaptureRunID: event.CaptureRunID, UserID: run.RuntimeUserID, Source: "local", StartedAt: event.OccurredAt, OccurredAt: event.OccurredAt, Status: event.Status}
		if value.UserID != "" {
			value.Source = "member"
		}
		content, known := ledger.options.Contents.items[event.SubjectID]
		value.RequestedModel, value.UpstreamModel = content.Request.RequestedModel, content.Request.EffectiveModel
		if content.Response != nil {
			convert := func(v exchangecontent.UsageValue) protocolcore.UsageValue {
				return protocolcore.UsageValue{Known: v.Known, Tokens: v.Tokens, Source: v.Source}
			}
			u := content.Response.Usage
			value.Usage = protocolcore.Usage{InputUncached: convert(u.InputUncached), CacheWrite: convert(u.CacheWrite), CacheRead: convert(u.CacheRead), Output: convert(u.Output), Reasoning: convert(u.Reasoning)}
		}
		identity, ok := ledger.options.Identities.items[event.SubjectID]
		if !ok && known {
			identity, _ = agentconversation.ClientIdentityFromProtocolEvidence(content.Request.ProtocolEvidence, "", content.RecordedAt)
		}
		value.Client, value.SessionID = identity.Client, identity.SessionID
		result = append(result, value)
	}
	return result, false, nil
}

func TestOwnerOverviewIncludesLocalAndManualWithoutCreatingUsers(t *testing.T) {
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	ledger := &observationLedger{items: []runtimeusage.Observation{
		{ExchangeID: "local", Source: "local", OccurredAt: now, Status: activity.StatusSucceeded, EnvironmentID: "general", EnvironmentName: "General", AccountID: "one", UpstreamModel: "unknown", Usage: protocolcore.Usage{Output: protocolcore.UsageValue{Known: true, Tokens: 0}}},
		{ExchangeID: "manual", Source: "manual", OccurredAt: now, Status: activity.StatusFailed, EnvironmentID: "inspect"},
	}}
	p, err := runtimeusage.New(runtimeusage.Options{Users: usersOf(), Runs: fakeRuns{}, Ledger: ledger, Clock: fixedClock{now}})
	if err != nil {
		t.Fatal(err)
	}
	report, err := p.Report(context.Background(), queryAround(t, now))
	if err != nil {
		t.Fatal(err)
	}
	if report.Total.AgentAPICalls != 2 || report.Total.Succeeded != 1 || report.Total.Failed != 1 || len(report.Users) != 0 || len(report.Sources) != 2 || len(report.Profiles) != 2 || len(report.Days) != 1 || report.Days[0].AgentAPICalls != 2 {
		t.Fatalf("report = %#v", report)
	}
	if report.Total.Tokens.Output.KnownCalls != 1 || report.Total.Tokens.Output.UnknownCalls != 1 || report.Total.Tokens.Output.Tokens != 0 {
		t.Fatalf("unknown is not zero: %#v", report.Total.Tokens)
	}
	if len(report.Models) != 2 {
		t.Fatal("an absent model must not merge with a model literally named unknown")
	}
}

type observationLedger struct {
	items    []runtimeusage.Observation
	selected runtimeuser.UserID
}

func (*observationLedger) RecordUsage(context.Context, runtimeusage.Observation) error { return nil }
func (*observationLedger) UsagePolicy(context.Context) (runtimeusage.CollectionPolicy, error) {
	return runtimeusage.CollectionPolicy{Enabled: true, RetentionDays: 90, Revision: 1}, nil
}
func (*observationLedger) SetUsagePolicy(_ context.Context, p runtimeusage.CollectionPolicy, _ time.Time) (runtimeusage.CollectionPolicy, error) {
	return p, nil
}
func (l *observationLedger) ListUsage(_ context.Context, _ runtimeusage.Query, user runtimeuser.UserID, _ time.Time, _ int) ([]runtimeusage.Observation, bool, error) {
	l.selected = user
	return l.items, false, nil
}
