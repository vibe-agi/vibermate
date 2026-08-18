// Package exchangecontent owns the optional, retention-bound conversation
// evidence for one semantic Exchange. It is deliberately separate from the
// body-free Activity, ConnectionEvent, and EgressAttempt journals.
package exchangecontent

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
)

const (
	MaxExchangeIDBytes = 512
	MaxEncodedBytes    = 32 << 20
)

var (
	ErrInvalidEvidence = errors.New("Exchange content evidence is invalid")
	ErrNotFound        = errors.New("Exchange content evidence was not found")
	ErrRuntimeStopping = errors.New("Exchange content runtime is stopping")

	unixHomePattern    = regexp.MustCompile(`(^|[\s("'=])/(?:Users|home)/[^/\s"'<>]+`)
	windowsHomePattern = regexp.MustCompile(
		`(?i)(^|[\s("'=])[a-z]:\\Users\\[^\\\s"'<>]+`,
	)
	bearerPattern = regexp.MustCompile(
		`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]{8,}`,
	)
	headerSecretPattern = regexp.MustCompile(
		`(?i)\b(authorization|proxy-authorization|cookie|set-cookie)[ \t]*[:=][ \t]*[^\s,;]+`,
	)
	providerSecretPattern = regexp.MustCompile(
		`\b(?:sk-ant-[A-Za-z0-9_-]{8,}|sk-[A-Za-z0-9_-]{16,})\b`,
	)
	urlUserInfoPattern = regexp.MustCompile(
		`(?i)\b(https?://)[^/@\s:]+:[^/@\s]+@`,
	)
)

type FrozenRef struct {
	EnvironmentID          string `json:"environmentId"`
	EnvironmentRevision    uint64 `json:"environmentRevision"`
	EnvironmentDigest      string `json:"environmentDigest"`
	ClientEndpointID       string `json:"clientEndpointId"`
	ClientEndpointRevision uint64 `json:"clientEndpointRevision"`
	ProtocolPlanID         string `json:"protocolPlanId"`
	ProtocolPlanRevision   uint64 `json:"protocolPlanRevision"`
	RouteID                string `json:"routeId"`
	RouteRevision          uint64 `json:"routeRevision"`
}

// ParentRef identifies the capture boundary that produced this evidence. It
// is deliberately narrower than a session claim: a CaptureRun is sufficient
// to scope exact transcript-prefix reuse, while a ManualCapture may carry
// several unrelated application sessions and therefore never authorizes an
// incremental presentation by itself.
type ParentRef struct {
	CaptureRunID    string `json:"captureRunId,omitempty"`
	ManualCaptureID string `json:"manualCaptureId,omitempty"`
}

func (ref ParentRef) Validate() error {
	if ref.CaptureRunID != "" && ref.ManualCaptureID != "" {
		return fmt.Errorf("%w: content parent is ambiguous", ErrInvalidEvidence)
	}
	for _, value := range []string{ref.CaptureRunID, ref.ManualCaptureID} {
		if value != "" && !validIdentity(value, 128) {
			return fmt.Errorf("%w: content parent is invalid", ErrInvalidEvidence)
		}
	}
	return nil
}

type RequestPresentationMode string

const (
	// RequestPresentationCheckpoint means the full frozen request is the first
	// trustworthy local view for this branch (including after compaction or a
	// history rewrite).
	RequestPresentationCheckpoint RequestPresentationMode = "checkpoint"
	// RequestPresentationIncremental means an exact previously delivered
	// transcript is a prefix and only the suffix is new in this Exchange.
	RequestPresentationIncremental RequestPresentationMode = "incremental"
	// RequestPresentationSameTranscript marks an exact replay with no new
	// client transcript messages.
	RequestPresentationSameTranscript RequestPresentationMode = "same_transcript"
)

// RequestPresentation is repository-derived local presentation metadata. It
// is intentionally excluded from canonical evidence JSON: the immutable full
// Record remains the authority, while a content-addressed repository may
// derive a compact view without changing the provider request or evidence
// digest.
type RequestPresentation struct {
	Mode                  RequestPresentationMode `json:"-"`
	InheritedMessageCount int                     `json:"-"`
}

// RequestView selects whether a read projection carries the complete frozen
// request or only the exact suffix that was not present in its proven base.
// It changes presentation only; Record remains the immutable authority.
type RequestView string

const (
	RequestViewFull        RequestView = "full"
	RequestViewIncremental RequestView = "incremental"
)

func (ref FrozenRef) Validate() error {
	if ref.EnvironmentID == "" || ref.EnvironmentRevision == 0 ||
		ref.EnvironmentDigest == "" || ref.ClientEndpointID == "" ||
		ref.ClientEndpointRevision == 0 || ref.ProtocolPlanID == "" ||
		ref.ProtocolPlanRevision == 0 || ref.RouteID == "" ||
		ref.RouteRevision == 0 {
		return fmt.Errorf("%w: frozen reference is incomplete", ErrInvalidEvidence)
	}
	digest, err := environment.ParseCandidateDigest(ref.EnvironmentDigest)
	if err != nil || digest.String() != ref.EnvironmentDigest {
		return fmt.Errorf("%w: Environment digest is invalid", ErrInvalidEvidence)
	}
	return nil
}

type Availability string

const (
	AvailabilityRecorded Availability = "recorded"
	AvailabilityOmitted  Availability = "omitted"
	BlockKindReasoning                = "reasoning"
)

type Block struct {
	Kind           string          `json:"kind"`
	Availability   Availability    `json:"availability"`
	Text           string          `json:"text,omitempty"`
	OriginalSize   int             `json:"originalSize"`
	CallID         string          `json:"callId,omitempty"`
	ToolName       string          `json:"toolName,omitempty"`
	ToolNamespace  string          `json:"toolNamespace,omitempty"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	ToolError      bool            `json:"toolError,omitempty"`
	ProviderSource string          `json:"providerSource,omitempty"`
	ProviderKind   string          `json:"providerKind,omitempty"`
	Fingerprint    string          `json:"fingerprint,omitempty"`
	Agent          *AgentContext   `json:"agent,omitempty"`
}

type Message struct {
	Role   string        `json:"role"`
	Blocks []Block       `json:"blocks"`
	Agent  *AgentContext `json:"agent,omitempty"`
}

type AgentContext struct {
	AgentName string `json:"agentName,omitempty"`
	Author    string `json:"author,omitempty"`
	Recipient string `json:"recipient,omitempty"`
}

type ToolDefinition struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

type Request struct {
	RequestedModel  string `json:"requestedModel"`
	EffectiveModel  string `json:"effectiveModel"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
	Stream          bool   `json:"stream"`
	// System is the dialect's top-level instruction parameter — Anthropic
	// `system`, OpenAI Responses `instructions` — recorded where the wire puts
	// it. It is per-request configuration, not conversation history, so it is
	// never synthesized into Messages and never becomes a transcript node. A
	// dialect without such a parameter, such as OpenAI Chat Completions, leaves
	// it empty and keeps its instruction message inside Messages.
	System           []Block                              `json:"system"`
	Messages         []Message                            `json:"messages"`
	Tools            []ToolDefinition                     `json:"tools"`
	ProtocolEvidence []protocolcore.ProtocolEvidenceValue `json:"protocolEvidence"`
}

// MarshalJSON keeps every repeated field an array at the exchange-content
// authority boundary. Control-plane callers can expose Request directly
// without copying its fields into a second DTO that would drift as the
// protocol-neutral model evolves.
func (request Request) MarshalJSON() ([]byte, error) {
	type wire Request
	return json.Marshal(wire(cloneRequest(request)))
}

type UsageValue struct {
	Known  bool   `json:"known"`
	Tokens int64  `json:"-"`
	Source string `json:"source,omitempty"`
}

// MarshalJSON preserves the distinction between an unknown usage value and a
// provider-observed zero. A known zero must remain present on the wire; using
// omitempty for Tokens would turn it into an internally contradictory
// {"known":true,"source":...} value.
func (value UsageValue) MarshalJSON() ([]byte, error) {
	if !value.Known {
		if value.Tokens != 0 || value.Source != "" {
			return nil, fmt.Errorf("%w: unknown usage has evidence", ErrInvalidEvidence)
		}
		return json.Marshal(struct {
			Known bool `json:"known"`
		}{Known: false})
	}
	if value.Tokens < 0 || value.Source == "" {
		return nil, fmt.Errorf("%w: known usage is incomplete", ErrInvalidEvidence)
	}
	return json.Marshal(struct {
		Known  bool   `json:"known"`
		Tokens int64  `json:"tokens"`
		Source string `json:"source"`
	}{Known: true, Tokens: value.Tokens, Source: value.Source})
}

func (value *UsageValue) UnmarshalJSON(encoded []byte) error {
	var wire struct {
		Known  *bool   `json:"known"`
		Tokens *int64  `json:"tokens"`
		Source *string `json:"source"`
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return fmt.Errorf("%w: decode usage: %v", ErrInvalidEvidence, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing usage JSON", ErrInvalidEvidence)
	}
	if wire.Known == nil {
		return fmt.Errorf("%w: usage knowledge is missing", ErrInvalidEvidence)
	}
	if !*wire.Known {
		if wire.Tokens != nil || wire.Source != nil {
			return fmt.Errorf("%w: unknown usage has evidence", ErrInvalidEvidence)
		}
		*value = UsageValue{}
		return nil
	}
	if wire.Tokens == nil || *wire.Tokens < 0 || wire.Source == nil || *wire.Source == "" {
		return fmt.Errorf("%w: known usage is incomplete", ErrInvalidEvidence)
	}
	*value = UsageValue{Known: true, Tokens: *wire.Tokens, Source: *wire.Source}
	return nil
}

type Usage struct {
	InputUncached UsageValue `json:"inputUncached"`
	CacheWrite    UsageValue `json:"cacheWrite"`
	CacheRead     UsageValue `json:"cacheRead"`
	Output        UsageValue `json:"output"`
	Reasoning     UsageValue `json:"reasoning"`
}

type Response struct {
	ID               string                               `json:"id"`
	RequestedModel   string                               `json:"requestedModel"`
	EffectiveModel   string                               `json:"effectiveModel"`
	ReportedModel    string                               `json:"reportedModel"`
	StopReason       string                               `json:"stopReason"`
	Blocks           []Block                              `json:"blocks"`
	Usage            Usage                                `json:"usage"`
	ProtocolEvidence []protocolcore.ProtocolEvidenceValue `json:"protocolEvidence"`
}

// MarshalJSON applies the same stable-array contract to response evidence.
func (response Response) MarshalJSON() ([]byte, error) {
	type wire Response
	return json.Marshal(wire(cloneResponse(response)))
}

// Record contains only the neutral, redacted semantic view. Provider
// extensions, request headers, credential material, proxy credentials, and
// raw wire bodies have no representation here.
type Record struct {
	ExchangeID   string                           `json:"exchangeId"`
	Parent       ParentRef                        `json:"parent"`
	Frozen       FrozenRef                        `json:"frozen"`
	Mode         environment.ContentRecordingMode `json:"mode"`
	RecordedAt   time.Time                        `json:"recordedAt"`
	ExpiresAt    time.Time                        `json:"expiresAt"`
	Request      Request                          `json:"request"`
	Response     *Response                        `json:"response,omitempty"`
	Presentation RequestPresentation              `json:"-"`
}

// Projection is a bounded read model derived from a retained Record. A full
// projection contains every request message. An incremental projection may
// contain only a verified suffix, including zero messages for an exact replay.
// TotalMessageCount and Presentation preserve the relationship to the full
// authority without making the partial value pretend to be a Record.
type Projection struct {
	ExchangeID        string                           `json:"exchangeId"`
	Parent            ParentRef                        `json:"parent"`
	Frozen            FrozenRef                        `json:"frozen"`
	Mode              environment.ContentRecordingMode `json:"mode"`
	RecordedAt        time.Time                        `json:"recordedAt"`
	ExpiresAt         time.Time                        `json:"expiresAt"`
	Request           Request                          `json:"request"`
	Response          *Response                        `json:"response,omitempty"`
	Presentation      RequestPresentation              `json:"-"`
	View              RequestView                      `json:"-"`
	TotalMessageCount int                              `json:"-"`
}

type RecordOption func(*Record) error

func WithParentRef(parent ParentRef) RecordOption {
	return func(record *Record) error {
		if err := parent.Validate(); err != nil {
			return err
		}
		record.Parent = parent
		return nil
	}
}

func NewRecord(
	exchangeID string,
	frozen FrozenRef,
	policy environment.ContentRecordingPolicy,
	recordedAt time.Time,
	request protocolcore.Request,
	response *protocolcore.Response,
	options ...RecordOption,
) (Record, error) {
	if policy.Mode == environment.ContentRecordingOff {
		return Record{}, fmt.Errorf("%w: recording is disabled", ErrInvalidEvidence)
	}
	if err := policy.Validate(); err != nil || recordedAt.IsZero() {
		return Record{}, fmt.Errorf("%w: recording policy or time is invalid", ErrInvalidEvidence)
	}
	if err := request.Validate(); err != nil {
		return Record{}, fmt.Errorf("%w: request: %v", ErrInvalidEvidence, err)
	}
	full := policy.Mode == environment.ContentRecordingFull
	record := Record{
		ExchangeID:   exchangeID,
		Frozen:       frozen,
		Mode:         policy.Mode,
		RecordedAt:   recordedAt.UTC(),
		ExpiresAt:    recordedAt.UTC().AddDate(0, 0, int(policy.RetentionDays)),
		Request:      requestView(request, full),
		Presentation: RequestPresentation{Mode: RequestPresentationCheckpoint},
	}
	for _, option := range options {
		if option == nil {
			return Record{}, fmt.Errorf("%w: record option is nil", ErrInvalidEvidence)
		}
		if err := option(&record); err != nil {
			return Record{}, err
		}
	}
	if response != nil {
		if err := response.Validate(); err != nil {
			return Record{}, fmt.Errorf("%w: response: %v", ErrInvalidEvidence, err)
		}
		view := responseView(*response, full)
		record.Response = &view
	}
	if err := record.Validate(); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (record Record) Validate() error {
	if !validIdentity(record.ExchangeID, MaxExchangeIDBytes) ||
		record.Parent.Validate() != nil || record.Frozen.Validate() != nil || record.RecordedAt.IsZero() ||
		record.ExpiresAt.IsZero() || !record.ExpiresAt.After(record.RecordedAt) ||
		(record.Mode != environment.ContentRecordingFull &&
			record.Mode != environment.ContentRecordingMetadataOnly) {
		return ErrInvalidEvidence
	}
	if err := record.Request.validate(record.Mode); err != nil {
		return err
	}
	if record.Response != nil {
		if err := record.Response.validate(record.Mode); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > MaxEncodedBytes {
		return fmt.Errorf("%w: encoded evidence exceeds its bound", ErrInvalidEvidence)
	}
	return nil
}

func (record Record) Clone() Record {
	cloned := record
	cloned.Request = cloneRequest(record.Request)
	if record.Response != nil {
		response := cloneResponse(*record.Response)
		cloned.Response = &response
	}
	return cloned
}

// Project derives a read model from an already verified full Record.
func Project(record Record, view RequestView) (Projection, error) {
	if err := record.Validate(); err != nil {
		return Projection{}, err
	}
	request := cloneRequest(record.Request)
	if view == RequestViewIncremental {
		request.Messages = record.IncrementalRequest()
	}
	projection := Projection{
		ExchangeID: record.ExchangeID, Parent: record.Parent, Frozen: record.Frozen,
		Mode: record.Mode, RecordedAt: record.RecordedAt, ExpiresAt: record.ExpiresAt,
		Request: request, Presentation: record.Presentation, View: view,
		TotalMessageCount: len(record.Request.Messages),
	}
	if record.Response != nil {
		response := cloneResponse(*record.Response)
		projection.Response = &response
	}
	if err := projection.Validate(); err != nil {
		return Projection{}, err
	}
	return projection.Clone(), nil
}

func (projection Projection) Validate() error {
	if !validIdentity(projection.ExchangeID, MaxExchangeIDBytes) ||
		projection.Parent.Validate() != nil || projection.Frozen.Validate() != nil ||
		projection.RecordedAt.IsZero() || projection.ExpiresAt.IsZero() ||
		!projection.ExpiresAt.After(projection.RecordedAt) ||
		(projection.Mode != environment.ContentRecordingFull &&
			projection.Mode != environment.ContentRecordingMetadataOnly) ||
		projection.TotalMessageCount < 1 ||
		projection.TotalMessageCount > protocolcore.MaxMessageCount+1 {
		return ErrInvalidEvidence
	}
	if err := projection.Request.validateProjection(projection.Mode); err != nil {
		return err
	}
	inherited := projection.Presentation.InheritedMessageCount
	if inherited < 0 || inherited > projection.TotalMessageCount {
		return ErrInvalidEvidence
	}
	switch projection.Presentation.Mode {
	case RequestPresentationCheckpoint:
		if inherited != 0 {
			return ErrInvalidEvidence
		}
	case RequestPresentationIncremental:
		if inherited == 0 || inherited >= projection.TotalMessageCount {
			return ErrInvalidEvidence
		}
	case RequestPresentationSameTranscript:
		if inherited != projection.TotalMessageCount {
			return ErrInvalidEvidence
		}
	default:
		return ErrInvalidEvidence
	}
	wantMessages := projection.TotalMessageCount
	switch projection.View {
	case RequestViewFull:
	case RequestViewIncremental:
		wantMessages -= inherited
	default:
		return ErrInvalidEvidence
	}
	if len(projection.Request.Messages) != wantMessages {
		return ErrInvalidEvidence
	}
	if projection.Response != nil {
		if err := projection.Response.validate(projection.Mode); err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(projection)
	if err != nil || len(encoded) > MaxEncodedBytes {
		return fmt.Errorf("%w: encoded projection exceeds its bound", ErrInvalidEvidence)
	}
	return nil
}

func (projection Projection) Clone() Projection {
	cloned := projection
	cloned.Request = cloneRequest(projection.Request)
	if projection.Response != nil {
		response := cloneResponse(*projection.Response)
		cloned.Response = &response
	}
	return cloned
}

func (record Record) IncrementalRequest() []Message {
	inherited := record.Presentation.InheritedMessageCount
	if inherited < 0 || inherited > len(record.Request.Messages) {
		inherited = 0
	}
	result := make([]Message, len(record.Request.Messages)-inherited)
	for index, message := range record.Request.Messages[inherited:] {
		result[index] = Message{Role: message.Role, Blocks: cloneBlocks(message.Blocks), Agent: cloneAgentContext(message.Agent)}
	}
	return result
}

func CanonicalJSON(record Record) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > MaxEncodedBytes {
		return nil, ErrInvalidEvidence
	}
	return encoded, nil
}

func DecodeCanonicalJSON(encoded []byte) (Record, error) {
	if len(encoded) == 0 || len(encoded) > MaxEncodedBytes {
		return Record{}, ErrInvalidEvidence
	}
	var record Record
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("%w: decode: %v", ErrInvalidEvidence, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Record{}, fmt.Errorf("%w: trailing JSON", ErrInvalidEvidence)
	}
	canonical, err := CanonicalJSON(record)
	if err != nil || !bytes.Equal(canonical, encoded) {
		return Record{}, fmt.Errorf("%w: JSON is not canonical", ErrInvalidEvidence)
	}
	return record.Clone(), nil
}

func (request Request) validate(mode environment.ContentRecordingMode) error {
	if len(request.Messages) == 0 {
		return fmt.Errorf("%w: request projection is incomplete", ErrInvalidEvidence)
	}
	return request.validateProjection(mode)
}

func (request Request) validateProjection(mode environment.ContentRecordingMode) error {
	if request.RequestedModel == "" || request.EffectiveModel == "" ||
		request.MaxOutputTokens < 0 ||
		len(request.System) > protocolcore.MaxContentBlocks ||
		len(request.Messages) > protocolcore.MaxMessageCount+1 ||
		len(request.Tools) > protocolcore.MaxToolCount {
		return fmt.Errorf("%w: request projection is incomplete", ErrInvalidEvidence)
	}
	for _, block := range request.System {
		if err := block.validate(mode); err != nil {
			return err
		}
	}
	for _, message := range request.Messages {
		switch protocolcore.Role(message.Role) {
		case protocolcore.RoleSystem, protocolcore.RoleDeveloper,
			protocolcore.RoleUser, protocolcore.RoleAssistant, protocolcore.RoleTool:
		default:
			return fmt.Errorf("%w: message role is unsupported", ErrInvalidEvidence)
		}
		if len(message.Blocks) == 0 || len(message.Blocks) > protocolcore.MaxContentBlocks {
			return fmt.Errorf("%w: message blocks are invalid", ErrInvalidEvidence)
		}
		if message.Agent != nil {
			context := protocolcore.AgentMessageContext{
				AgentName: message.Agent.AgentName,
				Author:    message.Agent.Author,
				Recipient: message.Agent.Recipient,
			}
			if err := context.Validate(); err != nil {
				return fmt.Errorf("%w: agent message context: %v", ErrInvalidEvidence, err)
			}
		}
		for _, block := range message.Blocks {
			if err := block.validate(mode); err != nil {
				return err
			}
		}
	}
	for _, tool := range request.Tools {
		if !validIdentity(tool.Name, protocolcore.MaxToolNameBytes) ||
			(tool.Namespace != "" && !validIdentity(tool.Namespace, protocolcore.MaxToolNamespaceBytes)) {
			return fmt.Errorf("%w: tool definition is invalid", ErrInvalidEvidence)
		}
	}
	if err := protocolcore.ValidateProtocolEvidence(request.ProtocolEvidence); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	return nil
}

func (response Response) validate(mode environment.ContentRecordingMode) error {
	if !validIdentity(response.ID, 512) || response.RequestedModel == "" ||
		response.EffectiveModel == "" || response.ReportedModel == "" ||
		len(response.Blocks) == 0 || len(response.Blocks) > protocolcore.MaxContentBlocks {
		return fmt.Errorf("%w: response projection is incomplete", ErrInvalidEvidence)
	}
	switch protocolcore.StopReason(response.StopReason) {
	case protocolcore.StopReasonEndTurn, protocolcore.StopReasonMaxTokens,
		protocolcore.StopReasonToolUse, protocolcore.StopReasonStopSequence:
	default:
		return fmt.Errorf("%w: stop reason is unsupported", ErrInvalidEvidence)
	}
	for _, block := range response.Blocks {
		if err := block.validate(mode); err != nil {
			return err
		}
	}
	if err := protocolcore.ValidateProtocolEvidence(response.ProtocolEvidence); err != nil {
		return fmt.Errorf("%w: response protocol evidence: %v", ErrInvalidEvidence, err)
	}
	for _, value := range []UsageValue{
		response.Usage.InputUncached, response.Usage.CacheWrite,
		response.Usage.CacheRead, response.Usage.Output, response.Usage.Reasoning,
	} {
		if value.Tokens < 0 || value.Known != (value.Source != "") ||
			(!value.Known && value.Tokens != 0) {
			return fmt.Errorf("%w: usage evidence is invalid", ErrInvalidEvidence)
		}
	}
	return nil
}

func (block Block) validate(mode environment.ContentRecordingMode) error {
	if block.OriginalSize < 0 {
		return ErrInvalidEvidence
	}
	expected := AvailabilityOmitted
	if mode == environment.ContentRecordingFull &&
		protocolcore.BlockKind(block.Kind) != protocolcore.BlockProviderExtension {
		expected = AvailabilityRecorded
	}
	if block.Availability != expected {
		return fmt.Errorf("%w: block availability contradicts recording mode", ErrInvalidEvidence)
	}
	if block.Availability == AvailabilityOmitted &&
		(block.Text != "" || len(block.Arguments) != 0) {
		return fmt.Errorf("%w: omitted block retains content", ErrInvalidEvidence)
	}
	if block.Agent != nil {
		context := protocolcore.AgentMessageContext{
			AgentName: block.Agent.AgentName,
			Author:    block.Agent.Author,
			Recipient: block.Agent.Recipient,
		}
		if err := context.Validate(); err != nil {
			return fmt.Errorf("%w: block agent context: %v", ErrInvalidEvidence, err)
		}
	}
	switch protocolcore.BlockKind(block.Kind) {
	case protocolcore.BlockText, protocolcore.BlockRefusal:
		if block.CallID != "" || block.ToolName != "" || block.ToolNamespace != "" || len(block.Arguments) != 0 || block.ToolError {
			return fmt.Errorf("%w: text block contains tool evidence", ErrInvalidEvidence)
		}
	case protocolcore.BlockToolCall:
		if !validIdentity(block.CallID, 512) || !validIdentity(block.ToolName, protocolcore.MaxToolNameBytes) || block.ToolError {
			return fmt.Errorf("%w: tool call metadata is invalid", ErrInvalidEvidence)
		}
		if len(block.Arguments) != 0 && !json.Valid(block.Arguments) {
			return fmt.Errorf("%w: tool arguments are invalid", ErrInvalidEvidence)
		}
		if block.ToolNamespace != "" && !validIdentity(block.ToolNamespace, protocolcore.MaxToolNamespaceBytes) {
			return fmt.Errorf("%w: tool call namespace is invalid", ErrInvalidEvidence)
		}
	case protocolcore.BlockToolResult:
		if !validIdentity(block.CallID, 512) || len(block.Arguments) != 0 ||
			(block.ToolNamespace == "") != (block.ToolName == "") ||
			(block.ToolNamespace != "" && !validIdentity(block.ToolNamespace, protocolcore.MaxToolNamespaceBytes)) ||
			(block.ToolName != "" && !validIdentity(block.ToolName, protocolcore.MaxToolNameBytes)) {
			return fmt.Errorf("%w: tool result metadata is invalid", ErrInvalidEvidence)
		}
	case protocolcore.BlockProviderExtension:
		if block.Availability != AvailabilityOmitted || block.CallID != "" ||
			block.ToolName != "" || block.ToolNamespace != "" || len(block.Arguments) != 0 || block.ToolError ||
			block.ProviderSource == "" || block.ProviderKind == "" ||
			!validFingerprint(block.Fingerprint) {
			return fmt.Errorf("%w: provider extension retained wire content", ErrInvalidEvidence)
		}
	case protocolcore.BlockKind(BlockKindReasoning):
		if block.CallID != "" || block.ToolName != "" || block.ToolNamespace != "" || len(block.Arguments) != 0 || block.ToolError ||
			block.ProviderSource == "" || block.ProviderKind == "" || block.Fingerprint != "" {
			return fmt.Errorf("%w: reasoning block contains tool evidence", ErrInvalidEvidence)
		}
	default:
		return fmt.Errorf("%w: block kind is unsupported", ErrInvalidEvidence)
	}
	return nil
}

func requestView(request protocolcore.Request, full bool) Request {
	messages := make([]Message, 0, len(request.Messages))
	for _, message := range request.Messages {
		messages = append(messages, Message{
			Role: string(message.Role), Blocks: blockViews(message.Blocks, full),
			Agent: agentContextView(message.Agent),
		})
	}
	tools := make([]ToolDefinition, 0, len(request.Tools))
	for _, tool := range request.Tools {
		tools = append(tools, ToolDefinition{Name: tool.Name})
	}
	for _, namespace := range request.ToolNamespaces {
		for _, tool := range namespace.Tools {
			tools = append(tools, ToolDefinition{Name: tool.Name, Namespace: namespace.Name})
		}
	}
	return Request{
		RequestedModel: request.RequestedModel, EffectiveModel: request.EffectiveModel,
		MaxOutputTokens: request.MaxOutputTokens, Stream: request.Stream,
		System:   blockViews(request.System, full),
		Messages: messages, Tools: tools,
		ProtocolEvidence: append([]protocolcore.ProtocolEvidenceValue{}, request.ProtocolEvidence...),
	}
}

func responseView(response protocolcore.Response, full bool) Response {
	blocks := responseBlockViews(response.Blocks, full)
	blocks = append(blocks, providerExtensionViews(response.ProviderExtensions, full)...)
	return Response{
		ID: response.ID, RequestedModel: response.RequestedModel,
		EffectiveModel: response.EffectiveModel, ReportedModel: response.ReportedModel,
		StopReason: string(response.StopReason), Blocks: blocks,
		Usage: usageView(response.Usage),
		ProtocolEvidence: append(
			[]protocolcore.ProtocolEvidenceValue{},
			response.ProtocolEvidence...,
		),
	}
}

func providerExtensionViews(extensions []protocolcore.ProviderExtension, full bool) []Block {
	result := make([]Block, 0, len(extensions))
	for _, extension := range extensions {
		rawBytes := 0
		for _, fragment := range extension.Fragments() {
			rawBytes += len(fragment)
		}
		text := providerReasoningText(extension)
		if full && text != "" {
			result = append(result, Block{
				Kind: BlockKindReasoning, Availability: AvailabilityRecorded,
				Text: sanitizeText(text), OriginalSize: len(text),
				ProviderSource: string(extension.Source()),
				ProviderKind:   string(extension.Kind()),
			})
		}
		if opaque := providerOpaqueEvidence(extension); len(opaque) != 0 {
			result = append(result, providerOpaqueBlock(extension, opaque))
		} else if text == "" || !full {
			result = append(result, providerOpaqueBlock(extension, bytes.Join(extension.Fragments(), nil)))
		}
	}
	return result
}

func providerReasoningText(extension protocolcore.ProviderExtension) string {
	var result strings.Builder
	for _, fragment := range extension.Fragments() {
		switch extension.Kind() {
		case protocolcore.ProviderExtensionThinking:
			var value struct {
				Thinking string `json:"thinking"`
			}
			if json.Unmarshal(fragment, &value) == nil {
				result.WriteString(value.Thinking)
			}
		case protocolcore.ProviderExtensionReasoningContent:
			var value string
			if json.Unmarshal(fragment, &value) == nil {
				result.WriteString(value)
				continue
			}
			var object struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(fragment, &object) == nil {
				result.WriteString(object.Text)
			}
		case protocolcore.ProviderExtensionReasoningSummary:
			var value struct {
				Text string `json:"text"`
			}
			if json.Unmarshal(fragment, &value) == nil {
				result.WriteString(value.Text)
			}
		}
	}
	return result.String()
}

func providerOpaqueEvidence(extension protocolcore.ProviderExtension) []byte {
	var opaque bytes.Buffer
	for _, fragment := range extension.Fragments() {
		switch extension.Kind() {
		case protocolcore.ProviderExtensionThinking:
			var value struct {
				Signature string `json:"signature"`
			}
			if json.Unmarshal(fragment, &value) == nil && value.Signature != "" {
				opaque.WriteString(value.Signature)
			}
		case protocolcore.ProviderExtensionReasoningEncryptedContent,
			protocolcore.ProviderExtensionAgentMessageEncryptedContent,
			protocolcore.ProviderExtensionRedactedThinking,
			protocolcore.ProviderExtensionAgentMessageImage,
			protocolcore.ProviderExtensionAgentMessageFile,
			protocolcore.ProviderExtensionAgentMessageScreenshot:
			opaque.Write(fragment)
		}
	}
	return opaque.Bytes()
}

func providerOpaqueBlock(extension protocolcore.ProviderExtension, opaque []byte) Block {
	digest := sha256.Sum256(opaque)
	return Block{
		Kind:           string(protocolcore.BlockProviderExtension),
		Availability:   AvailabilityOmitted,
		OriginalSize:   len(opaque),
		ProviderSource: string(extension.Source()),
		ProviderKind:   string(extension.Kind()),
		Fingerprint:    fmt.Sprintf("sha256:%x", digest[:]),
	}
}

func validFingerprint(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func blockViews(blocks []protocolcore.ContentBlock, full bool) []Block {
	return blockViewsWithProviderReasoning(blocks, full, false)
}

func responseBlockViews(blocks []protocolcore.ContentBlock, full bool) []Block {
	return blockViewsWithProviderReasoning(blocks, full, true)
}

func blockViewsWithProviderReasoning(
	blocks []protocolcore.ContentBlock,
	full bool,
	readableProviderReasoning bool,
) []Block {
	result := make([]Block, 0, len(blocks))
	for _, block := range blocks {
		view := Block{
			Kind: string(block.Kind), Availability: AvailabilityOmitted,
			Agent: agentContextView(block.Agent),
		}
		switch block.Kind {
		case protocolcore.BlockText:
			view.OriginalSize = len(block.Text)
			if full {
				view.Availability = AvailabilityRecorded
				view.Text = sanitizeText(block.Text)
			}
		case protocolcore.BlockRefusal:
			view.OriginalSize = len(block.Refusal)
			if full {
				view.Availability = AvailabilityRecorded
				view.Text = sanitizeText(block.Refusal)
			}
		case protocolcore.BlockToolCall:
			view.CallID = block.ToolCall.Key.WireID()
			view.ToolName = block.ToolCall.Name
			view.ToolNamespace = block.ToolCall.Namespace
			if block.ToolCall.EffectiveKind() == protocolcore.ToolKindFunction {
				view.OriginalSize = len(block.ToolCall.Arguments.Bytes())
				if full {
					view.Availability = AvailabilityRecorded
					view.Arguments = sanitizeJSON(block.ToolCall.Arguments.Bytes())
				}
			} else {
				view.OriginalSize = len(block.ToolCall.Input)
				if full {
					view.Availability = AvailabilityRecorded
					view.Text = sanitizeText(block.ToolCall.Input)
				}
			}
		case protocolcore.BlockToolResult:
			view.CallID = block.ToolResult.Key.WireID()
			view.ToolNamespace = block.ToolResult.Namespace
			view.ToolName = block.ToolResult.Name
			view.ToolError = block.ToolResult.IsError
			view.OriginalSize = len(block.ToolResult.Content)
			if full {
				view.Availability = AvailabilityRecorded
				view.Text = sanitizeText(block.ToolResult.Content)
			}
		case protocolcore.BlockProviderExtension:
			if text := providerReasoningText(block.ProviderExtension); readableProviderReasoning && full && text != "" {
				view.Kind = BlockKindReasoning
				view.Availability = AvailabilityRecorded
				view.Text = sanitizeText(text)
				view.OriginalSize = len(text)
				view.ProviderSource = string(block.ProviderExtension.Source())
				view.ProviderKind = string(block.ProviderExtension.Kind())
			} else {
				view = providerOpaqueBlock(
					block.ProviderExtension,
					bytes.Join(block.ProviderExtension.Fragments(), nil),
				)
				view.Agent = agentContextView(block.Agent)
			}
		}
		result = append(result, view)
	}
	return result
}

func usageView(usage protocolcore.Usage) Usage {
	convert := func(value protocolcore.UsageValue) UsageValue {
		return UsageValue{Known: value.Known, Tokens: value.Tokens, Source: value.Source}
	}
	return Usage{
		InputUncached: convert(usage.InputUncached), CacheWrite: convert(usage.CacheWrite),
		CacheRead: convert(usage.CacheRead), Output: convert(usage.Output),
		Reasoning: convert(usage.Reasoning),
	}
}

func sanitizeText(value string) string {
	if !utf8.ValidString(value) {
		return "[invalid text omitted]"
	}
	value = unixHomePattern.ReplaceAllString(value, "${1}~")
	value = windowsHomePattern.ReplaceAllString(value, "${1}~")
	value = headerSecretPattern.ReplaceAllString(value, "${1}: [redacted]")
	value = bearerPattern.ReplaceAllString(value, "Bearer [redacted]")
	value = providerSecretPattern.ReplaceAllString(value, "[redacted credential]")
	value = urlUserInfoPattern.ReplaceAllString(value, "${1}[redacted]@")
	return value
}

func sanitizeJSON(encoded []byte) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	value = sanitizeJSONValue(value)
	result, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"redacted":true}`)
	}
	return result
}

func sanitizeJSONValue(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeText(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sanitizeJSONValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if secretKey(key) {
				result[key] = "[redacted]"
				continue
			}
			result[key] = sanitizeJSONValue(item)
		}
		return result
	default:
		return value
	}
}

func secretKey(key string) bool {
	canonical := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	return strings.Contains(canonical, "authorization") ||
		strings.Contains(canonical, "api_key") ||
		strings.Contains(canonical, "apikey") ||
		strings.Contains(canonical, "password") ||
		strings.Contains(canonical, "secret") ||
		strings.Contains(canonical, "cookie") ||
		strings.Contains(canonical, "token")
}

func cloneRequest(value Request) Request {
	cloned := value
	cloned.System = cloneBlocks(value.System)
	cloned.Messages = make([]Message, len(value.Messages))
	for index, message := range value.Messages {
		cloned.Messages[index] = Message{
			Role: message.Role, Blocks: cloneBlocks(message.Blocks),
			Agent: cloneAgentContext(message.Agent),
		}
	}
	cloned.Tools = append([]ToolDefinition{}, value.Tools...)
	cloned.ProtocolEvidence = append([]protocolcore.ProtocolEvidenceValue{}, value.ProtocolEvidence...)
	return cloned
}

func agentContextView(context *protocolcore.AgentMessageContext) *AgentContext {
	if context == nil {
		return nil
	}
	return &AgentContext{
		AgentName: context.AgentName,
		Author:    context.Author,
		Recipient: context.Recipient,
	}
}

func cloneAgentContext(context *AgentContext) *AgentContext {
	if context == nil {
		return nil
	}
	cloned := *context
	return &cloned
}

func cloneResponse(value Response) Response {
	cloned := value
	cloned.Blocks = cloneBlocks(value.Blocks)
	cloned.ProtocolEvidence = append(
		[]protocolcore.ProtocolEvidenceValue{},
		value.ProtocolEvidence...,
	)
	return cloned
}

func cloneBlocks(value []Block) []Block {
	result := make([]Block, len(value))
	for index, block := range value {
		result[index] = block
		result[index].Arguments = append(json.RawMessage(nil), block.Arguments...)
		result[index].Agent = cloneAgentContext(block.Agent)
	}
	return result
}

func validIdentity(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
