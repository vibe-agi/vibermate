package desktopcontrol

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
)

const activityCursorPrefix = "v1:activity-requests:"
const conversationCursorPrefix = "v1:conversations:"

var errInvalidActivityQuery = errors.New("Activity query is invalid")

type FrozenEnvironmentRef struct {
	ID                     string `json:"id"`
	Revision               uint64 `json:"revision"`
	Digest                 string `json:"digest"`
	ClientEndpointID       string `json:"clientEndpointId"`
	ClientEndpointRevision uint64 `json:"clientEndpointRevision"`
	ProtocolPlanID         string `json:"protocolPlanId"`
	ProtocolPlanRevision   uint64 `json:"protocolPlanRevision"`
	RouteID                string `json:"routeId"`
	RouteRevision          uint64 `json:"routeRevision"`
	AccountID              string `json:"accountId,omitempty"`
	AccountRevision        uint64 `json:"accountRevision,omitempty"`
	CredentialEpoch        uint64 `json:"credentialEpoch,omitempty"`
}

type ActivitySummary struct {
	ID           string                  `json:"id"`
	OccurredAt   time.Time               `json:"occurredAt"`
	Kind         string                  `json:"kind"`
	Title        string                  `json:"title"`
	Status       string                  `json:"status"`
	ReasonCode   string                  `json:"reasonCode,omitempty"`
	Source       ActivitySourceRef       `json:"source"`
	Conversation ActivityConversationRef `json:"conversation"`
	Environment  FrozenEnvironmentRef    `json:"environment"`
	ParentRefs   ActivityParentRefs      `json:"parentRefs"`
}

// ActivityConversationRef is a flat, structural projection boundary. It does
// not claim a parent/child relationship between agents and it never contains
// prompt or response content.
type ActivityConversationRef struct {
	ID             string                            `json:"id"`
	DisplayName    string                            `json:"displayName,omitempty"`
	Kind           string                            `json:"kind"`
	Evidence       string                            `json:"evidence"`
	Actor          string                            `json:"actor,omitempty"`
	ClientIdentity *agentconversation.ClientIdentity `json:"clientIdentity,omitempty"`
}

func activityConversationRefOf(record activity.Record) ActivityConversationRef {
	if record.Conversation == nil {
		return ActivityConversationRef{}
	}
	return ActivityConversationRef{
		ID:          record.Conversation.ProjectionID,
		DisplayName: record.Conversation.DisplayName,
		Kind:        string(record.Conversation.Kind),
		Evidence:    string(record.Conversation.Evidence),
		Actor:       record.Conversation.Actor,
	}
}

func (ref ActivityConversationRef) Validate() error {
	if err := (agentconversation.Ref{
		ProjectionID: ref.ID,
		DisplayName:  ref.DisplayName,
		Kind:         agentconversation.Kind(ref.Kind),
		Evidence:     agentconversation.Evidence(ref.Evidence),
		Actor:        ref.Actor,
	}).Validate(); err != nil {
		return err
	}
	if ref.ClientIdentity != nil {
		if err := ref.ClientIdentity.Validate(); err != nil {
			return err
		}
		if ref.Actor != ref.ClientIdentity.ActorID {
			return errors.New("Conversation client actor does not match projection")
		}
	}
	return nil
}

type ActivitySourceRef struct {
	Kind        string `json:"kind"`
	DisplayName string `json:"displayName"`
	Recognition string `json:"recognition"`
}

type ActivityParentRefs struct {
	CaptureRunID    string `json:"captureRunId,omitempty"`
	ManualCaptureID string `json:"manualCaptureId,omitempty"`
	ConnectionID    string `json:"connectionId,omitempty"`
	ExchangeID      string `json:"exchangeId"`
}

func (summary ActivitySummary) Validate() error {
	if summary.ID == "" || summary.OccurredAt.IsZero() || summary.Kind != "exchange" ||
		summary.Title == "" || summary.ParentRefs.ExchangeID != summary.ID ||
		summary.Environment.ID == "" || summary.Environment.Revision == 0 ||
		summary.Environment.Digest == "" || summary.Environment.ClientEndpointID == "" ||
		summary.Environment.ClientEndpointRevision == 0 || summary.Environment.ProtocolPlanID == "" ||
		summary.Environment.ProtocolPlanRevision == 0 || summary.Environment.RouteID == "" ||
		summary.Environment.RouteRevision == 0 || summary.Conversation.Validate() != nil {
		return errors.New("Activity summary relationship is invalid")
	}
	return nil
}

type ActivityPage struct {
	Items      []ActivitySummary `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type ConversationSummary struct {
	Conversation    ActivityConversationRef `json:"conversation"`
	FirstObservedAt time.Time               `json:"firstObservedAt"`
	TurnCount       int                     `json:"turnCount"`
	Latest          ActivitySummary         `json:"latest"`
}

type ConversationPage struct {
	Items      []ConversationSummary `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

func conversationPageOf(page activity.ConversationPage) (ConversationPage, error) {
	view := ConversationPage{Items: make([]ConversationSummary, 0, len(page.Items))}
	for _, item := range page.Items {
		activities, err := activityPageOf(activity.Page{Items: []activity.Record{item.Latest}})
		if err != nil || len(activities.Items) != 1 || item.Validate() != nil {
			return ConversationPage{}, errors.New("Conversation projection is invalid")
		}
		view.Items = append(view.Items, ConversationSummary{
			Conversation:    activityConversationRefOf(item.Latest),
			FirstObservedAt: item.FirstOccurredAt,
			TurnCount:       item.TurnCount,
			Latest:          activities.Items[0],
		})
	}
	if page.NextBeforeFirstSequence != 0 {
		cursor, err := sequenceCursor(conversationCursorPrefix, page.NextBeforeFirstSequence)
		if err != nil {
			return ConversationPage{}, err
		}
		view.NextCursor = cursor
	}
	return view, nil
}

type ExchangeDetail struct {
	ID              string                            `json:"id"`
	Status          string                            `json:"status"`
	Environment     FrozenEnvironmentRef              `json:"environment"`
	ParentRefs      ActivityParentRefs                `json:"parentRefs"`
	Diagnosis       *ExchangeDiagnosis                `json:"diagnosis,omitempty"`
	ProcessingTrace ExchangeProcessingTrace           `json:"processingTrace"`
	Content         ExchangeContentDetail             `json:"content"`
	ClientIdentity  *agentconversation.ClientIdentity `json:"clientIdentity,omitempty"`
}

// ExchangeDiagnosis is deliberately structural. It identifies the side and
// shape that failed without carrying request values, provider text, or
// credentials into the control plane.
type ExchangeDiagnosis struct {
	ProviderStatus int    `json:"providerStatus,omitempty"`
	ProviderField  string `json:"providerField,omitempty"`
	ClientField    string `json:"clientField,omitempty"`
	ClientPath     string `json:"clientPath,omitempty"`
}

type ExchangeContentState string

const (
	ExchangeContentRecorded    ExchangeContentState = "recorded"
	ExchangeContentNotRecorded ExchangeContentState = "not_recorded"
)

type ExchangeContentDetail struct {
	State             ExchangeContentState         `json:"state"`
	Mode              string                       `json:"mode,omitempty"`
	RecordedAt        *time.Time                   `json:"recordedAt,omitempty"`
	ExpiresAt         *time.Time                   `json:"expiresAt,omitempty"`
	RequestProjection *ExchangeRequestProjection   `json:"requestProjection,omitempty"`
	AgentConversation *AgentConversationProjection `json:"agentConversation,omitempty"`
	Request           *exchangecontent.Request     `json:"request,omitempty"`
	Response          *exchangecontent.Response    `json:"response,omitempty"`
}

// AgentConversationProjection is a rebuildable relationship view over the
// immutable Exchange transcript. It never becomes a second conversation
// authority: managed runs remain scoped by CaptureRun, while a manual capture
// can only prove the current Exchange. Nodes and links are created only from
// explicit agent provenance or multi-agent lifecycle items carried by the
// source protocol.
type AgentConversationProjection struct {
	Scope         string                          `json:"scope"`
	Agents        []AgentConversationAgent        `json:"agents"`
	Relationships []AgentConversationRelationship `json:"relationships"`
	Actions       []AgentConversationAction       `json:"actions"`
}

type AgentConversationAgent struct {
	Name string `json:"name"`
}

type AgentConversationRelationship struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
}

type AgentConversationAction struct {
	CallID      string `json:"callId"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	SourceAgent string `json:"sourceAgent,omitempty"`
	ResultAgent string `json:"resultAgent,omitempty"`
	Attributed  bool   `json:"attributed"`
}

type ExchangeRequestProjection struct {
	View                  ExchangeContentView                     `json:"view"`
	Relationship          exchangecontent.RequestPresentationMode `json:"relationship"`
	InheritedMessageCount int                                     `json:"inheritedMessageCount"`
	TotalMessageCount     int                                     `json:"totalMessageCount"`
	FullSnapshotAvailable bool                                    `json:"fullSnapshotAvailable"`
}

type ExchangeContentView string

const (
	ExchangeContentViewIncremental ExchangeContentView = "incremental"
	ExchangeContentViewFull        ExchangeContentView = "full"
)

type ExchangeProcessingTrace struct {
	EgressProxyID string             `json:"egressProxyId,omitempty"`
	PluginRunIDs  []string           `json:"pluginRunIds"`
	Attempts      []egressaudit.View `json:"attempts"`
	Result        string             `json:"result"`
}

type activityListQuery struct {
	beforeSequence  int64
	limit           int
	captureRunID    string
	manualCaptureID string
	environmentID   string
	conversationID  string
}

func parseActivityListQuery(rawQuery string) (activityListQuery, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return activityListQuery{}, errInvalidActivityQuery
	}
	for name, entries := range values {
		if (name != "cursor" && name != "limit" && name != "captureRunId" && name != "manualCaptureId" && name != "environmentId" && name != "conversationId" && name != "kind") || len(entries) != 1 {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	query := activityListQuery{limit: 50}
	if entries, present := values["limit"]; present {
		query.limit, err = strconv.Atoi(entries[0])
		if err != nil || query.limit < 1 || query.limit > activity.MaxPageSize {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["cursor"]; present {
		query.beforeSequence, err = parseActivityCursor(entries[0])
		if err != nil {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["captureRunId"]; present {
		query.captureRunID = entries[0]
		if query.captureRunID == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["manualCaptureId"]; present {
		query.manualCaptureID = entries[0]
		if query.manualCaptureID == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if query.captureRunID != "" && query.manualCaptureID != "" {
		return activityListQuery{}, errInvalidActivityQuery
	}
	if entries, present := values["environmentId"]; present {
		query.environmentID = entries[0]
		if query.environmentID == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["conversationId"]; present {
		query.conversationID = entries[0]
		if query.conversationID == "" {
			return activityListQuery{}, errInvalidActivityQuery
		}
	}
	if entries, present := values["kind"]; present && entries[0] != "exchange" {
		return activityListQuery{}, errInvalidActivityQuery
	}
	if (activity.PageRequest{
		BeforeSequence:           query.beforeSequence,
		Limit:                    query.limit,
		CaptureRunID:             query.captureRunID,
		ManualCaptureID:          query.manualCaptureID,
		EnvironmentID:            query.environmentID,
		ConversationProjectionID: query.conversationID,
	}).Validate() != nil {
		return activityListQuery{}, errInvalidActivityQuery
	}
	return query, nil
}

func frozenEnvironmentRefOf(record activity.Record) FrozenEnvironmentRef {
	return FrozenEnvironmentRef{
		ID: record.EnvironmentID, Revision: record.EnvironmentRevision, Digest: record.EnvironmentDigest,
		ClientEndpointID: record.ClientEndpointID, ClientEndpointRevision: record.ClientEndpointRevision,
		ProtocolPlanID: record.ProtocolPlanID, ProtocolPlanRevision: record.ProtocolPlanRevision,
		RouteID: record.RouteID, RouteRevision: record.RouteRevision, AccountID: record.AccountID,
		AccountRevision: record.AccountRevision, CredentialEpoch: record.CredentialEpoch,
	}
}

func parentRefsOf(record activity.Record) ActivityParentRefs {
	return ActivityParentRefs{
		CaptureRunID: record.CaptureRunID, ManualCaptureID: record.ManualCaptureID,
		ConnectionID: record.ConnectionID, ExchangeID: record.SubjectID,
	}
}

func activityPageOf(page activity.Page) (ActivityPage, error) {
	view := ActivityPage{Items: make([]ActivitySummary, 0, len(page.Items))}
	for _, record := range page.Items {
		if !isExchangeActivity(record.Kind) || record.Validate() != nil {
			return ActivityPage{}, errors.New("Activity Exchange projection is invalid")
		}
		summary := ActivitySummary{
			ID: record.SubjectID, OccurredAt: record.OccurredAt, Kind: "exchange",
			Title: record.SourceDisplayName, Status: string(record.Status), ReasonCode: record.ReasonCode,
			Source:       ActivitySourceRef{Kind: string(record.SourceKind), DisplayName: record.SourceDisplayName, Recognition: string(record.SourceRecognition)},
			Conversation: activityConversationRefOf(record),
			Environment:  frozenEnvironmentRefOf(record), ParentRefs: parentRefsOf(record),
		}
		if summary.Validate() != nil {
			return ActivityPage{}, errors.New("Activity Exchange projection is invalid")
		}
		view.Items = append(view.Items, summary)
	}
	if page.NextBeforeSequence != 0 {
		cursor, err := activityCursor(page.NextBeforeSequence)
		if err != nil {
			return ActivityPage{}, err
		}
		view.NextCursor = cursor
	}
	return view, nil
}

func exchangeDetailOf(
	record activity.Record,
	egressPage egressaudit.Page,
	content *exchangecontent.Record,
	contentView ExchangeContentView,
) (ExchangeDetail, error) {
	if !isExchangeActivity(record.Kind) || record.Validate() != nil ||
		egressPage.NextCursor != "" ||
		(contentView != ExchangeContentViewIncremental && contentView != ExchangeContentViewFull) {
		return ExchangeDetail{}, errors.New("Exchange detail projection is incomplete")
	}
	ordered := make([]egressaudit.View, 0, len(egressPage.Items))
	proxyIDs := make(map[string]struct{})
	for _, item := range egressPage.Items {
		parent := item.Attempt.Parent()
		if parent.ExchangeID != record.SubjectID {
			return ExchangeDetail{}, errors.New("Exchange detail contains unrelated egress evidence")
		}
		ordered = append(ordered, egressaudit.ViewOf(item))
		if proxyID := item.Attempt.Decision().ProxyID; proxyID != "" {
			proxyIDs[proxyID] = struct{}{}
		}
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].Sequence < ordered[right].Sequence })
	result := record.ReasonCode
	if result == "" {
		result = string(record.Status)
	}
	detail := ExchangeDetail{
		ID: record.SubjectID, Status: string(record.Status), Environment: frozenEnvironmentRefOf(record), ParentRefs: parentRefsOf(record),
		ProcessingTrace: ExchangeProcessingTrace{PluginRunIDs: []string{}, Attempts: ordered, Result: result},
		Content:         ExchangeContentDetail{State: ExchangeContentNotRecorded},
	}
	if record.Diagnosis != nil && !record.Diagnosis.Empty() {
		detail.Diagnosis = &ExchangeDiagnosis{
			ProviderStatus: record.Diagnosis.ProviderStatus,
			ProviderField:  record.Diagnosis.ProviderField,
			ClientField:    record.Diagnosis.ClientField,
			ClientPath:     record.Diagnosis.ClientPath,
		}
	}
	if content != nil {
		if content.Validate() != nil || content.ExchangeID != record.SubjectID ||
			content.Parent.CaptureRunID != record.CaptureRunID ||
			content.Parent.ManualCaptureID != record.ManualCaptureID ||
			content.Frozen.EnvironmentID != record.EnvironmentID ||
			content.Frozen.EnvironmentRevision != record.EnvironmentRevision ||
			content.Frozen.EnvironmentDigest != record.EnvironmentDigest ||
			content.Frozen.ClientEndpointID != record.ClientEndpointID ||
			content.Frozen.ClientEndpointRevision != record.ClientEndpointRevision ||
			content.Frozen.ProtocolPlanID != record.ProtocolPlanID ||
			content.Frozen.ProtocolPlanRevision != record.ProtocolPlanRevision ||
			content.Frozen.RouteID != record.RouteID ||
			content.Frozen.RouteRevision != record.RouteRevision ||
			!validRequestPresentation(content.Presentation, len(content.Request.Messages)) {
			return ExchangeDetail{}, errors.New("Exchange content does not match frozen Activity evidence")
		}
		requestView := content.Request
		if contentView == ExchangeContentViewIncremental {
			requestView.Messages = content.IncrementalRequest()
		}
		recordedAt := content.RecordedAt
		expiresAt := content.ExpiresAt
		detail.Content = ExchangeContentDetail{
			State: ExchangeContentRecorded, Mode: string(content.Mode),
			RecordedAt: &recordedAt, ExpiresAt: &expiresAt,
			AgentConversation: agentConversationProjection(*content),
			RequestProjection: &ExchangeRequestProjection{
				View: contentView, Relationship: content.Presentation.Mode,
				InheritedMessageCount: content.Presentation.InheritedMessageCount,
				TotalMessageCount:     len(content.Request.Messages),
				FullSnapshotAvailable: content.Presentation.InheritedMessageCount > 0,
			},
			Request: &requestView,
		}
		if content.Response != nil {
			responseView := *content.Response
			detail.Content.Response = &responseView
		}
	}
	if len(proxyIDs) == 1 {
		for proxyID := range proxyIDs {
			detail.ProcessingTrace.EgressProxyID = proxyID
		}
	}
	return detail, nil
}

func agentConversationProjection(content exchangecontent.Record) *AgentConversationProjection {
	agentOrder := make([]string, 0)
	agentSet := make(map[string]struct{})
	relationships := make([]AgentConversationRelationship, 0)
	relationshipSet := make(map[string]struct{})
	actions := make([]AgentConversationAction, 0)
	actionIndexes := make(map[string]int)

	addAgent := func(name string) {
		if name == "" {
			return
		}
		if _, found := agentSet[name]; found {
			return
		}
		agentSet[name] = struct{}{}
		agentOrder = append(agentOrder, name)
	}
	addContext := func(agent *exchangecontent.AgentContext) {
		if agent == nil {
			return
		}
		addAgent(agent.AgentName)
		addAgent(agent.Author)
		addAgent(agent.Recipient)
		if agent.Author == "" || agent.Recipient == "" {
			return
		}
		key := agent.Author + "\x00" + agent.Recipient
		if _, found := relationshipSet[key]; found {
			return
		}
		relationshipSet[key] = struct{}{}
		relationships = append(relationships, AgentConversationRelationship{
			Source: agent.Author,
			Target: agent.Recipient,
			Kind:   "message",
		})
	}
	addAction := func(block exchangecontent.Block, agent *exchangecontent.AgentContext, allowUnattributedSubtask bool) {
		isExplicitMultiAgent := block.ToolNamespace == "multi_agent"
		isUnattributedSubtask := allowUnattributedSubtask && block.ToolNamespace == "" && strings.EqualFold(block.ToolName, "Agent")
		if !isExplicitMultiAgent && !isUnattributedSubtask {
			return
		}
		name := block.ToolName
		if isUnattributedSubtask {
			name = "subtask"
		}
		status := "requested"
		if block.Kind == "tool_result" {
			status = "completed"
			if block.ToolError {
				status = "failed"
			}
		}
		index, found := actionIndexes[block.CallID]
		if !found {
			actionIndexes[block.CallID] = len(actions)
			action := AgentConversationAction{
				CallID:     block.CallID,
				Name:       name,
				Status:     status,
				Attributed: isExplicitMultiAgent,
			}
			if agent != nil {
				if block.Kind == "tool_result" {
					action.ResultAgent = agent.AgentName
				} else {
					action.SourceAgent = agent.AgentName
				}
			}
			actions = append(actions, action)
			return
		}
		action := &actions[index]
		if block.Kind == "tool_result" {
			action.Status = status
			if action.Name == "" {
				action.Name = name
			}
			if agent != nil && agent.AgentName != "" {
				action.ResultAgent = agent.AgentName
			}
		} else if agent != nil && agent.AgentName != "" {
			action.SourceAgent = agent.AgentName
		}
	}
	observeBlocks := func(blocks []exchangecontent.Block, fallback *exchangecontent.AgentContext, allowUnattributedSubtask bool) {
		for _, block := range blocks {
			agent := block.Agent
			if agent == nil {
				agent = fallback
			}
			addContext(agent)
			addAction(block, agent, allowUnattributedSubtask)
		}
	}
	for _, message := range content.Request.Messages {
		addContext(message.Agent)
		observeBlocks(message.Blocks, message.Agent, true)
	}
	if content.Response != nil {
		observeBlocks(content.Response.Blocks, nil, false)
	}
	if len(agentOrder) == 0 && len(actions) == 0 {
		return nil
	}
	agents := make([]AgentConversationAgent, 0, len(agentOrder))
	for _, name := range agentOrder {
		agents = append(agents, AgentConversationAgent{Name: name})
	}
	scope := "exchange"
	if content.Parent.CaptureRunID != "" {
		scope = "capture_run"
	}
	return &AgentConversationProjection{
		Scope:         scope,
		Agents:        agents,
		Relationships: relationships,
		Actions:       actions,
	}
}

func isExchangeActivity(kind activity.Kind) bool {
	return kind == activity.KindExchangeStarted || kind == activity.KindExchangeCompleted
}

func validRequestPresentation(presentation exchangecontent.RequestPresentation, total int) bool {
	if total < 1 || presentation.InheritedMessageCount < 0 ||
		presentation.InheritedMessageCount > total {
		return false
	}
	switch presentation.Mode {
	case exchangecontent.RequestPresentationCheckpoint:
		return presentation.InheritedMessageCount == 0
	case exchangecontent.RequestPresentationIncremental:
		return presentation.InheritedMessageCount > 0 &&
			presentation.InheritedMessageCount < total
	case exchangecontent.RequestPresentationSameTranscript:
		return presentation.InheritedMessageCount == total
	default:
		return false
	}
}

func parseExchangeContentView(rawQuery string) (ExchangeContentView, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", errInvalidActivityQuery
	}
	if len(values) == 0 {
		return ExchangeContentViewIncremental, nil
	}
	entries, present := values["contentView"]
	if !present || len(values) != 1 || len(entries) != 1 {
		return "", errInvalidActivityQuery
	}
	view := ExchangeContentView(entries[0])
	if view != ExchangeContentViewIncremental && view != ExchangeContentViewFull {
		return "", errInvalidActivityQuery
	}
	return view, nil
}

func activityCursor(sequence int64) (string, error) {
	return sequenceCursor(activityCursorPrefix, sequence)
}

func sequenceCursor(prefix string, sequence int64) (string, error) {
	if sequence <= 0 {
		return "", errInvalidActivityQuery
	}
	payload := prefix + strconv.FormatInt(sequence, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)), nil
}

func parseActivityCursor(value string) (int64, error) {
	return parseSequenceCursor(value, activityCursorPrefix)
}

func parseConversationCursor(value string) (int64, error) {
	return parseSequenceCursor(value, conversationCursorPrefix)
}

func parseSequenceCursor(value, prefix string) (int64, error) {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, " \t\r\n=") {
		return 0, errInvalidActivityQuery
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return 0, errInvalidActivityQuery
	}
	payload, found := strings.CutPrefix(string(decoded), prefix)
	if !found {
		return 0, errInvalidActivityQuery
	}
	sequence, err := strconv.ParseInt(payload, 10, 64)
	if err != nil || sequence <= 0 {
		return 0, errInvalidActivityQuery
	}
	canonical, err := sequenceCursor(prefix, sequence)
	if err != nil || canonical != value {
		return 0, errInvalidActivityQuery
	}
	return sequence, nil
}

func (handler *Handler) getExchange(writer http.ResponseWriter, request *http.Request) {
	contentView, err := parseExchangeContentView(request.URL.RawQuery)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	exchangeID := request.PathValue("exchangeId")
	record, err := handler.activities.GetExchange(request.Context(), exchangeID)
	switch {
	case errors.Is(err, activity.ErrExchangeNotFound):
		writeProblem(writer, http.StatusNotFound, ReasonExchangeNotFound)
		return
	case errors.Is(err, activity.ErrInvalidEvent):
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	case err != nil:
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	egressPage, err := handler.egress.List(request.Context(), egressaudit.PageRequest{Limit: egressaudit.MaxPageLimit, ExchangeID: exchangeID})
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	var content *exchangecontent.Record
	contentRecord, contentErr := handler.contents.Get(request.Context(), exchangeID)
	switch {
	case contentErr == nil:
		content = &contentRecord
	case errors.Is(contentErr, exchangecontent.ErrNotFound):
	default:
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	detail, err := exchangeDetailOf(record, egressPage, content, contentView)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
		return
	}
	if handler.conversationIndexer != nil {
		handler.refreshConversationIndex(request.Context(), activity.ConversationIndexRequest{
			Limit:        1,
			CaptureRunID: record.CaptureRunID,
		})
		identity, identityErr := handler.conversationIndexer.Identity(
			request.Context(),
			record.SubjectID,
		)
		switch {
		case identityErr == nil:
			cloned := identity.Clone()
			detail.ClientIdentity = &cloned
		case errors.Is(identityErr, activity.ErrExchangeNotFound):
		default:
			writeProblem(writer, http.StatusServiceUnavailable, ReasonRuntimeUnavailable)
			return
		}
	}
	writeJSON(writer, http.StatusOK, detail)
}
