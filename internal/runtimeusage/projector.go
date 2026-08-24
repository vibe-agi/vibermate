// Package runtimeusage projects retained runtime evidence into an operator
// usage report. It never invents missing token counts or model identities.
package runtimeusage

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

const (
	ReportSchema       = "vibermate-runtime-usage-report-v1"
	maxCaptureRuns     = 10_000
	maxExchangeRecords = 100_000
)

type Clock interface{ Now() time.Time }
type UserReader interface {
	List(context.Context) ([]runtimeuser.User, error)
}
type CaptureRunReader interface {
	ListRuns(context.Context, capturerun.PageRequest) (capturerun.Page, error)
}
type ActivityReader interface {
	ListExchanges(context.Context, activity.PageRequest) (activity.Page, error)
}
type ContentReader interface {
	Get(context.Context, string) (exchangecontent.Record, error)
}
type IdentityReader interface {
	GetConversationIdentity(context.Context, string) (agentconversation.ClientIdentity, error)
}

type Options struct {
	Users      UserReader
	Runs       CaptureRunReader
	Activities ActivityReader
	Contents   ContentReader
	Identities IdentityReader
	Clock      Clock
}

type Projector struct{ options Options }

func New(options Options) (*Projector, error) {
	if options.Users == nil || options.Runs == nil || options.Activities == nil ||
		options.Contents == nil || options.Identities == nil || options.Clock == nil {
		return nil, errors.New("Runtime usage dependencies are incomplete")
	}
	return &Projector{options: options}, nil
}

type Report struct {
	Schema      string      `json:"schema"`
	GeneratedAt time.Time   `json:"generatedAt"`
	Truncated   bool        `json:"truncated"`
	Users       []UserUsage `json:"users"`
}

type UserUsage struct {
	UserID                  runtimeuser.UserID  `json:"userId"`
	Username                string              `json:"username"`
	State                   runtimeuser.State   `json:"state"`
	CaptureRuns             int                 `json:"captureRuns"`
	ActiveRuns              int                 `json:"activeRuns"`
	Turns                   int                 `json:"turns"`
	Succeeded               int                 `json:"succeeded"`
	Failed                  int                 `json:"failed"`
	Canceled                int                 `json:"canceled"`
	ContentUnavailableTurns int                 `json:"contentUnavailableTurns"`
	ModelUnavailableTurns   int                 `json:"modelUnavailableTurns"`
	Tokens                  TokenUsage          `json:"tokens"`
	LatestContext           *ContextRef         `json:"latestContext,omitempty"`
	LastActivityAt          *time.Time          `json:"lastActivityAt,omitempty"`
	Models                  []ModelUsage        `json:"models"`
	Contexts                []ContextUsage      `json:"contexts"`
	AgentSessions           []AgentSessionUsage `json:"agentSessions"`
}

type ContextRef struct {
	LoginSessionID runtimeuser.LoginSessionID `json:"loginSessionId"`
	DeviceName     string                     `json:"deviceName"`
	MachineID      string                     `json:"machineId"`
	WorkspaceID    string                     `json:"workspaceId,omitempty"`
	WorkspaceLabel string                     `json:"workspaceLabel,omitempty"`
	ObservedAt     time.Time                  `json:"observedAt"`
}

type ContextUsage struct {
	LoginSessionID runtimeuser.LoginSessionID `json:"loginSessionId"`
	DeviceName     string                     `json:"deviceName"`
	MachineID      string                     `json:"machineId"`
	WorkspaceID    string                     `json:"workspaceId,omitempty"`
	WorkspaceLabel string                     `json:"workspaceLabel,omitempty"`
	CaptureRuns    int                        `json:"captureRuns"`
	ActiveRuns     int                        `json:"activeRuns"`
	Turns          int                        `json:"turns"`
	Succeeded      int                        `json:"succeeded"`
	Failed         int                        `json:"failed"`
	Canceled       int                        `json:"canceled"`
	Tokens         TokenUsage                 `json:"tokens"`
	LastActivityAt *time.Time                 `json:"lastActivityAt,omitempty"`
}

type ModelUsage struct {
	RequestedModel string     `json:"requestedModel"`
	UpstreamModel  string     `json:"upstreamModel"`
	Turns          int        `json:"turns"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	Canceled       int        `json:"canceled"`
	Tokens         TokenUsage `json:"tokens"`
}

type AgentSessionUsage struct {
	Client         string     `json:"client"`
	SessionID      string     `json:"sessionId"`
	CaptureRuns    int        `json:"captureRuns"`
	Turns          int        `json:"turns"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	Canceled       int        `json:"canceled"`
	Tokens         TokenUsage `json:"tokens"`
	LastActivityAt time.Time  `json:"lastActivityAt"`
}

type TokenAggregate struct {
	Tokens       int64 `json:"tokens"`
	KnownTurns   int   `json:"knownTurns"`
	UnknownTurns int   `json:"unknownTurns"`
}

type TokenUsage struct {
	InputUncached TokenAggregate `json:"inputUncached"`
	CacheWrite    TokenAggregate `json:"cacheWrite"`
	CacheRead     TokenAggregate `json:"cacheRead"`
	Output        TokenAggregate `json:"output"`
	Reasoning     TokenAggregate `json:"reasoning"`
}

type userAccumulator struct {
	view      UserUsage
	contexts  map[string]*ContextUsage
	models    map[string]*ModelUsage
	sessions  map[string]*sessionAccumulator
	latestRun time.Time
}

type sessionAccumulator struct {
	view AgentSessionUsage
	runs map[string]struct{}
}

func (projector *Projector) Report(ctx context.Context) (Report, error) {
	if projector == nil || ctx == nil {
		return Report{}, errors.New("Runtime usage request is invalid")
	}
	users, err := projector.options.Users.List(ctx)
	if err != nil {
		return Report{}, err
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	accumulators := make(map[runtimeuser.UserID]*userAccumulator, len(users))
	for _, user := range users {
		accumulators[user.ID] = &userAccumulator{
			view: UserUsage{UserID: user.ID, Username: user.Username, State: user.State,
				Models: []ModelUsage{}, Contexts: []ContextUsage{}, AgentSessions: []AgentSessionUsage{}},
			contexts: map[string]*ContextUsage{}, models: map[string]*ModelUsage{},
			sessions: map[string]*sessionAccumulator{},
		}
	}
	runs, truncated, err := projector.listRuns(ctx)
	if err != nil {
		return Report{}, err
	}
	exchangeCount := 0
	for _, run := range runs {
		accumulator := accumulators[run.RuntimeUserID]
		if accumulator == nil || run.LoginSessionID == "" {
			continue
		}
		accumulator.addRun(run)
		remaining := maxExchangeRecords - exchangeCount
		if remaining <= 0 {
			truncated = true
			break
		}
		records, recordsTruncated, readErr := projector.listExchanges(ctx, run.ID, remaining)
		if readErr != nil {
			return Report{}, readErr
		}
		if recordsTruncated {
			truncated = true
		}
		for _, record := range records {
			exchangeCount++
			if err := projector.addExchange(ctx, accumulator, run, record); err != nil {
				return Report{}, err
			}
		}
		if truncated && exchangeCount >= maxExchangeRecords {
			break
		}
	}
	report := Report{Schema: ReportSchema, GeneratedAt: projector.options.Clock.Now().UTC(), Truncated: truncated,
		Users: make([]UserUsage, 0, len(users))}
	for _, user := range users {
		accumulator := accumulators[user.ID]
		accumulator.finish()
		report.Users = append(report.Users, accumulator.view)
	}
	return report, nil
}

func (projector *Projector) listRuns(ctx context.Context) ([]capturerun.View, bool, error) {
	result := make([]capturerun.View, 0, capturerun.MaxPageLimit)
	var cursor *capturerun.PageCursor
	for len(result) < maxCaptureRuns {
		page, err := projector.options.Runs.ListRuns(ctx, capturerun.PageRequest{
			Limit: capturerun.MaxPageLimit, Cursor: cursor,
		})
		if err != nil {
			return nil, false, err
		}
		if len(page.Items) == 0 {
			return result, false, nil
		}
		remaining := maxCaptureRuns - len(result)
		if len(page.Items) > remaining {
			result = append(result, page.Items[:remaining]...)
			return result, true, nil
		}
		result = append(result, page.Items...)
		if len(page.Items) < capturerun.MaxPageLimit {
			return result, false, nil
		}
		last := page.Items[len(page.Items)-1]
		cursor = &capturerun.PageCursor{Running: active(last.State), UpdatedAt: last.UpdatedAt, AfterID: last.ID}
	}
	return result, true, nil
}

func (projector *Projector) listExchanges(ctx context.Context, runID string, limit int) ([]activity.Record, bool, error) {
	result := make([]activity.Record, 0)
	var before int64
	for len(result) < limit {
		pageLimit := activity.MaxPageSize
		if remaining := limit - len(result); remaining < pageLimit {
			pageLimit = remaining
		}
		page, err := projector.options.Activities.ListExchanges(ctx, activity.PageRequest{
			BeforeSequence: before, Limit: pageLimit, CaptureRunID: runID,
		})
		if err != nil {
			return nil, false, err
		}
		result = append(result, page.Items...)
		if page.NextBeforeSequence == 0 {
			return result, false, nil
		}
		before = page.NextBeforeSequence
	}
	return result, true, nil
}

func (projector *Projector) addExchange(ctx context.Context, user *userAccumulator, run capturerun.View, record activity.Record) error {
	context := user.contexts[contextKey(run)]
	user.view.Turns++
	context.Turns++
	addStatus(&user.view.Succeeded, &user.view.Failed, &user.view.Canceled, record.Status)
	addStatus(&context.Succeeded, &context.Failed, &context.Canceled, record.Status)
	setLatest(&user.view.LastActivityAt, record.OccurredAt)
	setLatest(&context.LastActivityAt, record.OccurredAt)

	content, contentErr := projector.options.Contents.Get(ctx, record.SubjectID)
	contentKnown := contentErr == nil
	if contentErr != nil && !errors.Is(contentErr, exchangecontent.ErrNotFound) {
		return contentErr
	}
	usage := exchangecontent.Usage{}
	if !contentKnown {
		user.view.ContentUnavailableTurns++
		user.view.ModelUnavailableTurns++
	} else {
		if content.Response != nil {
			usage = content.Response.Usage
		}
		if content.Request.RequestedModel == "" || content.Request.EffectiveModel == "" {
			user.view.ModelUnavailableTurns++
		} else {
			model := user.model(content.Request.RequestedModel, content.Request.EffectiveModel)
			model.Turns++
			addStatus(&model.Succeeded, &model.Failed, &model.Canceled, record.Status)
			model.Tokens.add(usage)
		}
	}
	user.view.Tokens.add(usage)
	context.Tokens.add(usage)

	identity, identityErr := projector.options.Identities.GetConversationIdentity(ctx, record.SubjectID)
	if errors.Is(identityErr, activity.ErrExchangeNotFound) && contentKnown {
		responseID := ""
		if content.Response != nil {
			responseID = content.Response.ID
		}
		if derived, ok := agentconversation.ClientIdentityFromProtocolEvidence(
			content.Request.ProtocolEvidence, responseID, content.RecordedAt,
		); ok {
			identity, identityErr = derived, nil
		}
	}
	if identityErr != nil && !errors.Is(identityErr, activity.ErrExchangeNotFound) {
		return identityErr
	}
	if identityErr == nil && identity.Client != "" && identity.SessionID != "" {
		session := user.session(identity.Client, identity.SessionID)
		session.view.Turns++
		addStatus(&session.view.Succeeded, &session.view.Failed, &session.view.Canceled, record.Status)
		session.view.Tokens.add(usage)
		if record.OccurredAt.After(session.view.LastActivityAt) {
			session.view.LastActivityAt = record.OccurredAt
		}
		session.runs[run.ID] = struct{}{}
	}
	return nil
}

func (user *userAccumulator) addRun(run capturerun.View) {
	user.view.CaptureRuns++
	isActive := active(run.State)
	if isActive {
		user.view.ActiveRuns++
	}
	key := contextKey(run)
	context := user.contexts[key]
	if context == nil {
		context = &ContextUsage{LoginSessionID: run.LoginSessionID, DeviceName: run.DeviceName,
			MachineID: run.MachineID, WorkspaceID: run.WorkspaceID, WorkspaceLabel: run.WorkspaceLabel}
		user.contexts[key] = context
	}
	context.CaptureRuns++
	if isActive {
		context.ActiveRuns++
	}
	if user.view.LatestContext == nil || run.UpdatedAt.After(user.latestRun) {
		user.latestRun = run.UpdatedAt
		user.view.LatestContext = &ContextRef{LoginSessionID: run.LoginSessionID,
			DeviceName: run.DeviceName, MachineID: run.MachineID, WorkspaceID: run.WorkspaceID,
			WorkspaceLabel: run.WorkspaceLabel, ObservedAt: run.UpdatedAt}
	}
}

func (user *userAccumulator) model(requested, upstream string) *ModelUsage {
	key := requested + "\x00" + upstream
	value := user.models[key]
	if value == nil {
		value = &ModelUsage{RequestedModel: requested, UpstreamModel: upstream}
		user.models[key] = value
	}
	return value
}

func (user *userAccumulator) session(client, id string) *sessionAccumulator {
	key := client + "\x00" + id
	value := user.sessions[key]
	if value == nil {
		value = &sessionAccumulator{view: AgentSessionUsage{Client: client, SessionID: id}, runs: map[string]struct{}{}}
		user.sessions[key] = value
	}
	return value
}

func (user *userAccumulator) finish() {
	for _, value := range user.models {
		user.view.Models = append(user.view.Models, *value)
	}
	sort.Slice(user.view.Models, func(i, j int) bool {
		if user.view.Models[i].Turns != user.view.Models[j].Turns {
			return user.view.Models[i].Turns > user.view.Models[j].Turns
		}
		if user.view.Models[i].RequestedModel != user.view.Models[j].RequestedModel {
			return user.view.Models[i].RequestedModel < user.view.Models[j].RequestedModel
		}
		return user.view.Models[i].UpstreamModel < user.view.Models[j].UpstreamModel
	})
	for _, value := range user.contexts {
		user.view.Contexts = append(user.view.Contexts, *value)
	}
	sort.Slice(user.view.Contexts, func(i, j int) bool {
		left, right := user.view.Contexts[i], user.view.Contexts[j]
		if left.LastActivityAt != nil && right.LastActivityAt != nil && !left.LastActivityAt.Equal(*right.LastActivityAt) {
			return left.LastActivityAt.After(*right.LastActivityAt)
		}
		if (left.LastActivityAt != nil) != (right.LastActivityAt != nil) {
			return left.LastActivityAt != nil
		}
		return contextUsageKey(left) < contextUsageKey(right)
	})
	for _, value := range user.sessions {
		value.view.CaptureRuns = len(value.runs)
		user.view.AgentSessions = append(user.view.AgentSessions, value.view)
	}
	sort.Slice(user.view.AgentSessions, func(i, j int) bool {
		left, right := user.view.AgentSessions[i], user.view.AgentSessions[j]
		if !left.LastActivityAt.Equal(right.LastActivityAt) {
			return left.LastActivityAt.After(right.LastActivityAt)
		}
		if left.Client != right.Client {
			return left.Client < right.Client
		}
		return left.SessionID < right.SessionID
	})
}

func (usage *TokenUsage) add(value exchangecontent.Usage) {
	usage.InputUncached.add(value.InputUncached)
	usage.CacheWrite.add(value.CacheWrite)
	usage.CacheRead.add(value.CacheRead)
	usage.Output.add(value.Output)
	usage.Reasoning.add(value.Reasoning)
}

func (aggregate *TokenAggregate) add(value exchangecontent.UsageValue) {
	if value.Known {
		aggregate.Tokens += value.Tokens
		aggregate.KnownTurns++
	} else {
		aggregate.UnknownTurns++
	}
}

func addStatus(succeeded, failed, canceled *int, status activity.Status) {
	switch status {
	case activity.StatusSucceeded:
		*succeeded++
	case activity.StatusFailed:
		*failed++
	case activity.StatusCanceled:
		*canceled++
	}
}

func setLatest(target **time.Time, value time.Time) {
	if value.IsZero() || (*target != nil && !value.After(**target)) {
		return
	}
	copy := value
	*target = &copy
}

func active(state capturerun.State) bool {
	return state == capturerun.StateCreated || state == capturerun.StateAttached
}

func contextKey(run capturerun.View) string {
	return string(run.LoginSessionID) + "\x00" + run.MachineID + "\x00" + run.WorkspaceID
}

func contextUsageKey(value ContextUsage) string {
	return string(value.LoginSessionID) + "\x00" + value.MachineID + "\x00" + value.WorkspaceID
}
