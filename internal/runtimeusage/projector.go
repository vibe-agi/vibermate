// Package runtimeusage collects body-free usage independently of recording.
// It never invents missing token counts or model identities.
package runtimeusage

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/modelcatalog"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/runtimeuser"
)

const (
	ReportSchema       = "vibermate-runtime-usage-report-v1"
	maxCaptureRuns     = 10_000
	maxExchangeRecords = 100_000
	maxReportedUsers   = 200
	maxUserModels      = 50
	maxUserContexts    = 100
	maxUserSessions    = 100
	maxReportDetails   = 5_000
)

type Clock interface{ Now() time.Time }
type UserReader interface {
	List(context.Context) ([]runtimeuser.User, error)
}
type CaptureRunReader interface {
	ListRuns(context.Context, capturerun.PageRequest) (capturerun.Page, error)
}
type Options struct {
	Users  UserReader
	Runs   CaptureRunReader
	Ledger Repository
	Clock  Clock
	Prices *modelcatalog.ReferencePrices
}

type Projector struct{ options Options }

func New(options Options) (*Projector, error) {
	if options.Users == nil || options.Runs == nil || options.Ledger == nil || options.Clock == nil {
		return nil, errors.New("Runtime usage dependencies are incomplete")
	}
	return &Projector{options: options}, nil
}

type Report struct {
	Schema      string           `json:"schema"`
	GeneratedAt time.Time        `json:"generatedAt"`
	Period      Period           `json:"period"`
	Truncated   bool             `json:"truncated"`
	Days        []DayUsage       `json:"days"`
	Users       []UserUsage      `json:"users"`
	Collection  CollectionPolicy `json:"collection"`
	Total       GroupUsage       `json:"total"`
	Sources     []GroupUsage     `json:"sources"`
	Profiles    []GroupUsage     `json:"profiles"`
	Accounts    []GroupUsage     `json:"accounts"`
	Models      []GroupUsage     `json:"models"`
	Callers     []GroupUsage     `json:"callers"`
	Projects    []GroupUsage     `json:"projects"`
	Pricing     PricingInfo      `json:"pricing"`
}

type GroupUsage struct {
	Dimension         string       `json:"dimension,omitempty"`
	Evidence          string       `json:"evidence,omitempty"`
	Children          []GroupUsage `json:"children,omitempty"`
	ChildrenTruncated bool         `json:"childrenTruncated,omitempty"`
	ID                string       `json:"id"`
	Label             string       `json:"label"`
	AgentAPICalls     int          `json:"agentApiCalls"`
	Succeeded         int          `json:"succeeded"`
	Failed            int          `json:"failed"`
	Canceled          int          `json:"canceled"`
	Tokens            TokenUsage   `json:"tokens"`
	Cost              CostEstimate `json:"cost"`
}

func (group *GroupUsage) add(record Observation) {
	group.AgentAPICalls++
	addStatus(&group.Succeeded, &group.Failed, &group.Canceled, record.Status)
	group.Tokens.add(record.Usage)
}

func (projector *Projector) SetCollectionPolicy(ctx context.Context, policy CollectionPolicy) (CollectionPolicy, error) {
	return projector.options.Ledger.SetUsagePolicy(ctx, policy, projector.options.Clock.Now().UTC())
}

type UserUsage struct {
	UserID                   runtimeuser.UserID  `json:"userId"`
	Username                 string              `json:"username"`
	State                    runtimeuser.State   `json:"state"`
	CaptureRuns              int                 `json:"captureRuns"`
	ActiveRuns               int                 `json:"activeRuns"`
	AgentAPICalls            int                 `json:"agentApiCalls"`
	Succeeded                int                 `json:"succeeded"`
	Failed                   int                 `json:"failed"`
	Canceled                 int                 `json:"canceled"`
	ModelUnavailableCalls    int                 `json:"modelUnavailableCalls"`
	Tokens                   TokenUsage          `json:"tokens"`
	LatestContext            *ContextRef         `json:"latestContext,omitempty"`
	LastActivityAt           *time.Time          `json:"lastActivityAt,omitempty"`
	Days                     []DayUsage          `json:"days"`
	Models                   []ModelUsage        `json:"models"`
	Contexts                 []ContextUsage      `json:"contexts"`
	AgentSessions            []AgentSessionUsage `json:"agentSessions"`
	DailyAgentAPICallWarning int64               `json:"dailyAgentApiCallWarning"`
	DailyTokenWarning        int64               `json:"dailyTokenWarning"`
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
	AgentAPICalls  int                        `json:"agentApiCalls"`
	Succeeded      int                        `json:"succeeded"`
	Failed         int                        `json:"failed"`
	Canceled       int                        `json:"canceled"`
	Tokens         TokenUsage                 `json:"tokens"`
	LastActivityAt *time.Time                 `json:"lastActivityAt,omitempty"`
}

type ModelUsage struct {
	RequestedModel string     `json:"requestedModel"`
	UpstreamModel  string     `json:"upstreamModel"`
	AgentAPICalls  int        `json:"agentApiCalls"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	Canceled       int        `json:"canceled"`
	Tokens         TokenUsage `json:"tokens"`
}

type AgentSessionUsage struct {
	Client         string     `json:"client"`
	SessionID      string     `json:"sessionId"`
	CaptureRuns    int        `json:"captureRuns"`
	AgentAPICalls  int        `json:"agentApiCalls"`
	Succeeded      int        `json:"succeeded"`
	Failed         int        `json:"failed"`
	Canceled       int        `json:"canceled"`
	Tokens         TokenUsage `json:"tokens"`
	LastActivityAt time.Time  `json:"lastActivityAt"`
}

type TokenAggregate struct {
	Tokens       int64 `json:"tokens"`
	KnownCalls   int   `json:"knownCalls"`
	UnknownCalls int   `json:"unknownCalls"`
}

type TokenUsage struct {
	InputUncached TokenAggregate `json:"inputUncached"`
	CacheWrite    TokenAggregate `json:"cacheWrite"`
	CacheRead     TokenAggregate `json:"cacheRead"`
	Output        TokenAggregate `json:"output"`
	Reasoning     TokenAggregate `json:"reasoning"`
}

// DayUsage is one non-empty calendar day in the requested Period. The report
// is sparse by design: absence means no retained terminal Agent API Call on
// that day, while the evidence counters distinguish a known zero from missing
// evidence.
type DayUsage struct {
	Date                  string       `json:"date"`
	AgentAPICalls         int          `json:"agentApiCalls"`
	Succeeded             int          `json:"succeeded"`
	Failed                int          `json:"failed"`
	Canceled              int          `json:"canceled"`
	ModelUnavailableCalls int          `json:"modelUnavailableCalls"`
	Tokens                TokenUsage   `json:"tokens"`
	Cost                  CostEstimate `json:"cost"`
}

type userAccumulator struct {
	view      UserUsage
	days      map[string]*DayUsage
	contexts  map[string]*ContextUsage
	models    map[string]*ModelUsage
	sessions  map[string]*sessionAccumulator
	latestRun time.Time
}

type sessionAccumulator struct {
	view AgentSessionUsage
	runs map[string]struct{}
}

func (projector *Projector) Report(ctx context.Context, query Query) (Report, error) {
	return projector.report(ctx, query, "")
}

func (projector *Projector) report(
	ctx context.Context,
	query Query,
	selectedUserID runtimeuser.UserID,
) (Report, error) {
	if projector == nil || ctx == nil || !query.valid() {
		return Report{}, errors.New("Runtime usage request is invalid")
	}
	users, err := projector.options.Users.List(ctx)
	if err != nil {
		return Report{}, err
	}
	if selectedUserID != "" {
		selected := make([]runtimeuser.User, 0, 1)
		for _, user := range users {
			if user.ID == selectedUserID {
				selected = append(selected, user)
				break
			}
		}
		if len(selected) == 0 {
			return Report{}, errors.New("Runtime usage user is unavailable")
		}
		users = selected
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	accumulators := make(map[runtimeuser.UserID]*userAccumulator, len(users))
	for _, user := range users {
		accumulators[user.ID] = &userAccumulator{
			view: UserUsage{UserID: user.ID, Username: user.Username, State: user.State,
				Days: []DayUsage{}, Models: []ModelUsage{}, Contexts: []ContextUsage{}, AgentSessions: []AgentSessionUsage{},
				DailyAgentAPICallWarning: user.Policy.DailyAgentAPICallWarning,
				DailyTokenWarning:        user.Policy.DailyTokenWarning},
			days:     map[string]*DayUsage{},
			contexts: map[string]*ContextUsage{}, models: map[string]*ModelUsage{},
			sessions: map[string]*sessionAccumulator{},
		}
	}
	runs, truncated, err := projector.listRuns(ctx, selectedUserID)
	if err != nil {
		return Report{}, err
	}
	runsByID := make(map[string]capturerun.View, len(runs))
	countedRuns := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		runsByID[run.ID] = run
		accumulator := accumulators[run.RuntimeUserID]
		if accumulator == nil || run.LoginSessionID == "" {
			continue
		}
		if query.contains(run.UpdatedAt) {
			accumulator.addRun(run)
			countedRuns[run.ID] = struct{}{}
		}
	}
	// ponytail: a bounded indexed scan keeps one aggregation path; move the
	// group reducers to SQL if a report regularly exceeds 100,000 calls.
	records, recordsTruncated, err := projector.options.Ledger.ListUsage(ctx, query, selectedUserID, projector.options.Clock.Now().UTC(), maxExchangeRecords)
	if err != nil {
		return Report{}, err
	}
	truncated = truncated || recordsTruncated
	policy, err := projector.options.Ledger.UsagePolicy(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Schema: ReportSchema, GeneratedAt: projector.options.Clock.Now().UTC(),
		Period: query.period, Truncated: truncated, Days: []DayUsage{}, Collection: policy,
		Users: make([]UserUsage, 0, len(users)), Total: GroupUsage{ID: "all", Label: "all"}}
	var prices modelcatalog.PriceSnapshot
	if len(records) > 0 {
		prices = projector.options.Prices.Snapshot(ctx)
	}
	report.Pricing = pricingInfo(prices)
	groups := [6]map[string]*groupAccumulator{}
	groupBudgets := [6]int{}
	for index := range groups {
		groups[index] = map[string]*groupAccumulator{}
		groupBudgets[index] = maxReportDetails
	}
	days := map[string]*DayUsage{}
	for _, record := range records {
		if !query.contains(record.OccurredAt) || (selectedUserID != "" && record.UserID != selectedUserID) {
			continue
		}
		report.Total.add(record)
		cost := estimateCost(prices, record)
		report.Total.Cost.add(cost)
		attribution := record.Attribution
		if attribution == nil {
			// Older observations can use immutable launch metadata, never today's
			// working tree or the identity of the viewer of this report.
			run := runsByID[record.CaptureRunID]
			if run.RuntimeUserID == record.UserID {
				attribution = CaptureAttribution(run)
			} else {
				attribution = &Attribution{}
			}
			if record.UserID != "" {
				attribution.CallerID, attribution.CallerKind = string(record.UserID), "member"
				if user := accumulators[record.UserID]; user != nil {
					attribution.CallerLabel = user.view.Username
				}
			}
		}
		model := groupLabel{"model", record.UpstreamModel, record.UpstreamModel, ""}
		caller := groupLabel{"caller", attribution.CallerID, attribution.CallerLabel, attribution.CallerKind}
		project, branch := groupLabel{dimension: "project"}, groupLabel{dimension: "branch"}
		if git := attribution.GitAtLaunch; git != nil {
			project = groupLabel{"project", attribution.ProjectID, git.RepositoryName, "launch_snapshot"}
			branch = groupLabel{"branch", "branch:" + git.Branch, git.Branch, "launch_snapshot"}
			if git.Detached {
				branch = groupLabel{"branch", "detached", "", "detached"}
			}
		}
		for index, path := range [][]groupLabel{
			{{"source", record.Source, record.Source, ""}, model},
			{{"profile", record.EnvironmentID, record.EnvironmentName, ""}, model},
			{{"account", record.AccountID, record.AccountName, ""}, model},
			{model, caller}, {caller, model}, {project, branch, caller, model},
		} {
			addGroup(groups[index], path, record, cost, &groupBudgets[index], &report.Truncated)
		}
		date := query.day(record.OccurredAt)
		day := days[date]
		if day == nil {
			day = &DayUsage{Date: date}
			days[date] = day
		}
		day.AgentAPICalls++
		addStatus(&day.Succeeded, &day.Failed, &day.Canceled, record.Status)
		day.Tokens.add(record.Usage)
		day.Cost.add(cost)
		if record.RequestedModel == "" || record.UpstreamModel == "" {
			day.ModelUnavailableCalls++
		}
		accumulator := accumulators[record.UserID]
		if accumulator == nil {
			continue
		}
		run, runKnown := runsByID[record.CaptureRunID]
		if _, counted := countedRuns[run.ID]; runKnown && !counted && run.RuntimeUserID == record.UserID {
			accumulator.addRun(run)
			countedRuns[run.ID] = struct{}{}
		}
		accumulator.addExchange(run, record, date)
		accumulator.days[date].Cost.add(cost)
	}
	for _, user := range users {
		accumulator := accumulators[user.ID]
		accumulator.finish()
		report.Users = append(report.Users, accumulator.view)
	}
	for _, day := range days {
		report.Days = append(report.Days, *day)
	}
	sort.Slice(report.Days, func(i, j int) bool { return report.Days[i].Date < report.Days[j].Date })
	destinations := []*[]GroupUsage{&report.Sources, &report.Profiles, &report.Accounts, &report.Models, &report.Callers, &report.Projects}
	for index, destination := range destinations {
		*destination = finishGroups(groups[index])
	}
	sort.Slice(report.Users, func(left, right int) bool {
		return userUsageLess(report.Users[left], report.Users[right])
	})
	if len(report.Users) > maxReportedUsers {
		report.Users = report.Users[:maxReportedUsers]
		report.Truncated = true
	}
	detailBudget := maxReportDetails
	for index := range report.Users {
		user := &report.Users[index]
		var trimmed bool
		user.Models, trimmed = boundedDetails(user.Models, maxUserModels, &detailBudget)
		report.Truncated = report.Truncated || trimmed
		user.Contexts, trimmed = boundedDetails(user.Contexts, maxUserContexts, &detailBudget)
		report.Truncated = report.Truncated || trimmed
		user.AgentSessions, trimmed = boundedDetails(
			user.AgentSessions, maxUserSessions, &detailBudget,
		)
		report.Truncated = report.Truncated || trimmed
	}
	return report, nil
}

// ReportForUser returns the same evidence semantics as Report while ensuring
// that no other Runtime User identity or aggregate leaves the module.
func (projector *Projector) ReportForUser(
	ctx context.Context,
	query Query,
	userID runtimeuser.UserID,
) (Report, error) {
	if !userID.Valid() {
		return Report{}, errors.New("Runtime usage user is invalid")
	}
	return projector.report(ctx, query, userID)
}

func userUsageLess(left, right UserUsage) bool {
	if left.AgentAPICalls != right.AgentAPICalls {
		return left.AgentAPICalls > right.AgentAPICalls
	}
	if left.CaptureRuns != right.CaptureRuns {
		return left.CaptureRuns > right.CaptureRuns
	}
	if left.LastActivityAt != nil && right.LastActivityAt != nil &&
		!left.LastActivityAt.Equal(*right.LastActivityAt) {
		return left.LastActivityAt.After(*right.LastActivityAt)
	}
	if (left.LastActivityAt != nil) != (right.LastActivityAt != nil) {
		return left.LastActivityAt != nil
	}
	return left.Username < right.Username
}

func boundedDetails[T any](items []T, perUserLimit int, budget *int) ([]T, bool) {
	limit := min(len(items), perUserLimit, max(*budget, 0))
	trimmed := limit < len(items)
	*budget -= limit
	return items[:limit], trimmed
}

func (projector *Projector) listRuns(ctx context.Context, userID runtimeuser.UserID) ([]capturerun.View, bool, error) {
	result := make([]capturerun.View, 0, capturerun.MaxPageLimit)
	var cursor *capturerun.PageCursor
	for len(result) < maxCaptureRuns {
		page, err := projector.options.Runs.ListRuns(ctx, capturerun.PageRequest{
			Limit: capturerun.MaxPageLimit, Cursor: cursor, RuntimeUserID: userID,
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
		cursor = &capturerun.PageCursor{
			Running: active(last.State), ActivityAt: last.ActivityTime(),
			AfterID: last.ID, IncludeAtActivityAt: true,
		}
	}
	return result, true, nil
}

func (user *userAccumulator) addExchange(
	run capturerun.View,
	record Observation,
	date string,
) {
	context := user.contexts[contextKey(run)]
	day := user.day(date)
	user.view.AgentAPICalls++
	day.AgentAPICalls++
	addStatus(&user.view.Succeeded, &user.view.Failed, &user.view.Canceled, record.Status)
	addStatus(&day.Succeeded, &day.Failed, &day.Canceled, record.Status)
	setLatest(&user.view.LastActivityAt, record.OccurredAt)
	if context != nil {
		context.AgentAPICalls++
		addStatus(&context.Succeeded, &context.Failed, &context.Canceled, record.Status)
		setLatest(&context.LastActivityAt, record.OccurredAt)
		context.Tokens.add(record.Usage)
	}
	if record.RequestedModel == "" || record.UpstreamModel == "" {
		user.view.ModelUnavailableCalls++
		day.ModelUnavailableCalls++
	} else {
		model := user.model(record.RequestedModel, record.UpstreamModel)
		model.AgentAPICalls++
		addStatus(&model.Succeeded, &model.Failed, &model.Canceled, record.Status)
		model.Tokens.add(record.Usage)
	}
	user.view.Tokens.add(record.Usage)
	day.Tokens.add(record.Usage)
	if record.Client != "" && record.SessionID != "" {
		session := user.session(record.Client, record.SessionID)
		session.view.AgentAPICalls++
		addStatus(&session.view.Succeeded, &session.view.Failed, &session.view.Canceled, record.Status)
		session.view.Tokens.add(record.Usage)
		if record.OccurredAt.After(session.view.LastActivityAt) {
			session.view.LastActivityAt = record.OccurredAt
		}
		if record.CaptureRunID != "" {
			session.runs[record.CaptureRunID] = struct{}{}
		}
	}
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

func (user *userAccumulator) day(date string) *DayUsage {
	value := user.days[date]
	if value == nil {
		value = &DayUsage{Date: date}
		user.days[date] = value
	}
	return value
}

func (user *userAccumulator) finish() {
	for _, value := range user.days {
		user.view.Days = append(user.view.Days, *value)
	}
	sort.Slice(user.view.Days, func(i, j int) bool {
		return user.view.Days[i].Date < user.view.Days[j].Date
	})
	for _, value := range user.models {
		user.view.Models = append(user.view.Models, *value)
	}
	sort.Slice(user.view.Models, func(i, j int) bool {
		if user.view.Models[i].AgentAPICalls != user.view.Models[j].AgentAPICalls {
			return user.view.Models[i].AgentAPICalls > user.view.Models[j].AgentAPICalls
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

func (usage *TokenUsage) add(value protocolcore.Usage) {
	usage.InputUncached.add(value.InputUncached)
	usage.CacheWrite.add(value.CacheWrite)
	usage.CacheRead.add(value.CacheRead)
	usage.Output.add(value.Output)
	usage.Reasoning.add(value.Reasoning)
}

func (usage *TokenUsage) addAggregate(value TokenUsage) {
	usage.InputUncached.addAggregate(value.InputUncached)
	usage.CacheWrite.addAggregate(value.CacheWrite)
	usage.CacheRead.addAggregate(value.CacheRead)
	usage.Output.addAggregate(value.Output)
	usage.Reasoning.addAggregate(value.Reasoning)
}

func (aggregate *TokenAggregate) add(value protocolcore.UsageValue) {
	if value.Known {
		if value.Tokens < 0 || value.Tokens > math.MaxInt64-aggregate.Tokens {
			aggregate.UnknownCalls++
			return
		}
		aggregate.Tokens += value.Tokens
		aggregate.KnownCalls++
	} else {
		aggregate.UnknownCalls++
	}
}

func (aggregate *TokenAggregate) addAggregate(value TokenAggregate) {
	if value.Tokens < 0 || value.Tokens > math.MaxInt64-aggregate.Tokens {
		aggregate.UnknownCalls += value.KnownCalls + value.UnknownCalls
		return
	}
	aggregate.Tokens += value.Tokens
	aggregate.KnownCalls += value.KnownCalls
	aggregate.UnknownCalls += value.UnknownCalls
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
