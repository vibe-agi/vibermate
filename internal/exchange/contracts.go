// Package exchange executes one immutable Access plan against the protocol and
// provider boundaries. It owns Exchange admission, Attempt commit accounting,
// and downstream publication, but it does not own an ingress listener.
package exchange

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
)

const MaxExchangeIdentityBytes = 512

type ReasonCode string

const (
	ReasonInvalidExchangeRequest        ReasonCode = "invalid_exchange_request"
	ReasonUnsupportedClientInput        ReasonCode = "unsupported_client_input"
	ReasonAccessPlanUnavailable         ReasonCode = "access_plan_unavailable"
	ReasonIngressBindingStale           ReasonCode = "ingress_binding_stale"
	ReasonOfflineHoldUnavailable        ReasonCode = "offline_hold_unavailable"
	ReasonUnsupportedAccessPlan         ReasonCode = "unsupported_access_plan"
	ReasonProviderRequestInvalid        ReasonCode = "provider_request_invalid"
	ReasonProviderCredentialUnavailable ReasonCode = "provider_credential_unavailable"
	ReasonProviderTransportFailed       ReasonCode = "provider_transport_failed"
	ReasonProviderResponseIdle          ReasonCode = "provider_response_idle"
	ReasonProviderStatusRejected        ReasonCode = "provider_status_rejected"
	ReasonProviderResponseInvalid       ReasonCode = "provider_response_invalid"
	ReasonTransportRetryExhausted       ReasonCode = "transport_retry_exhausted"
	ReasonToolDecisionRejected          ReasonCode = "tool_decision_rejected"
	ReasonToolDecisionUnavailable       ReasonCode = "tool_decision_unavailable"
	ReasonDownstreamCommitFailed        ReasonCode = "downstream_commit_failed"
	ReasonDownstreamDisconnected        ReasonCode = "downstream_disconnect"
	ReasonExchangeCanceled              ReasonCode = "exchange_canceled"
	ReasonExchangeRuntimeStopping       ReasonCode = "exchange_runtime_stopping"
	ReasonDownstreamFailureAborted      ReasonCode = "downstream_failure_aborted"
)

// ProviderField identifies only request fields emitted by the production
// protocol adapter. It never carries provider-supplied text.
type ProviderField string

const (
	ProviderFieldUnknown             ProviderField = ""
	ProviderFieldModel               ProviderField = "model"
	ProviderFieldMessages            ProviderField = "messages"
	ProviderFieldTools               ProviderField = "tools"
	ProviderFieldToolChoice          ProviderField = "tool_choice"
	ProviderFieldParallelToolCalls   ProviderField = "parallel_tool_calls"
	ProviderFieldMaxCompletionTokens ProviderField = "max_completion_tokens"
	ProviderFieldMaxTokens           ProviderField = "max_tokens"
	ProviderFieldReasoningEffort     ProviderField = "reasoning_effort"
	ProviderFieldTemperature         ProviderField = "temperature"
	ProviderFieldTopP                ProviderField = "top_p"
	ProviderFieldStop                ProviderField = "stop"
	ProviderFieldStream              ProviderField = "stream"
	ProviderFieldStreamOptions       ProviderField = "stream_options"
	ProviderFieldResponseFormat      ProviderField = "response_format"
	ProviderFieldN                   ProviderField = "n"
)

func knownProviderField(value string) ProviderField {
	switch ProviderField(value) {
	case ProviderFieldModel,
		ProviderFieldMessages,
		ProviderFieldTools,
		ProviderFieldToolChoice,
		ProviderFieldParallelToolCalls,
		ProviderFieldMaxCompletionTokens,
		ProviderFieldMaxTokens,
		ProviderFieldReasoningEffort,
		ProviderFieldTemperature,
		ProviderFieldTopP,
		ProviderFieldStop,
		ProviderFieldStream,
		ProviderFieldStreamOptions,
		ProviderFieldResponseFormat,
		ProviderFieldN:
		return ProviderField(value)
	default:
		return ProviderFieldUnknown
	}
}

// ProviderResponseIssue identifies a closed response-envelope failure class.
// It never carries provider-supplied text or response content.
type ProviderResponseIssue string

const (
	ProviderResponseIssueUnknown     ProviderResponseIssue = ""
	ProviderResponseIssueContentType ProviderResponseIssue = "content_type"
)

type ClientField string

const (
	ClientFieldUnknown                   ClientField = ""
	ClientFieldModel                     ClientField = "model"
	ClientFieldMaxTokens                 ClientField = "max_tokens"
	ClientFieldMessages                  ClientField = "messages"
	ClientFieldSystem                    ClientField = "system"
	ClientFieldMetadata                  ClientField = "metadata"
	ClientFieldStopSequences             ClientField = "stop_sequences"
	ClientFieldStream                    ClientField = "stream"
	ClientFieldTemperature               ClientField = "temperature"
	ClientFieldTopP                      ClientField = "top_p"
	ClientFieldTopK                      ClientField = "top_k"
	ClientFieldTools                     ClientField = "tools"
	ClientFieldToolChoice                ClientField = "tool_choice"
	ClientFieldThinking                  ClientField = "thinking"
	ClientFieldServiceTier               ClientField = "service_tier"
	ClientFieldCacheControl              ClientField = "cache_control"
	ClientFieldOutputConfig              ClientField = "output_config"
	ClientFieldOutputConfigUnknown       ClientField = "output_config_unknown"
	ClientFieldOutputConfigEffort        ClientField = "output_config_effort"
	ClientFieldOutputConfigEffortNone    ClientField = "output_config_effort_none"
	ClientFieldOutputConfigEffortMinimal ClientField = "output_config_effort_minimal"
	ClientFieldOutputConfigEffortInvalid ClientField = "output_config_effort_invalid"
	ClientFieldOutputConfigFormat        ClientField = "output_config_format"
	ClientFieldOutputConfigTaskBudget    ClientField = "output_config_task_budget"
	ClientFieldContainer                 ClientField = "container"
	ClientFieldInferenceGeo              ClientField = "inference_geo"
	ClientFieldContextManagement         ClientField = "context_management"
	ClientFieldMCPServers                ClientField = "mcp_servers"
	ClientFieldSpeed                     ClientField = "speed"
	ClientFieldFallbacks                 ClientField = "fallbacks"
	ClientFieldDiagnostics               ClientField = "diagnostics"
	ClientFieldOutputFormat              ClientField = "output_format"
)

var (
	ErrRuntimeStopping      = errors.New("Exchange runtime is stopping")
	ErrToolRejected         = errors.New("tool decision rejected")
	ErrProviderSemanticIdle = errors.New(
		"provider stream exceeded its semantic progress timeout",
	)
)

// Failure carries a stable language-independent classification. Error details
// are developer evidence and must never contain credentials or response bodies.
type Failure struct {
	Code           ReasonCode
	ExchangeID     string
	ProviderStatus int
	ProviderField  ProviderField
	ClientField    ClientField
	ProtocolReason protocolcore.Reason
	ResponseIssue  ProviderResponseIssue
	cause          error
}

func (failure *Failure) Error() string {
	if failure == nil {
		return "<nil>"
	}
	message := fmt.Sprintf(
		"Exchange failed: code=%s exchangeId=%q",
		failure.Code,
		failure.ExchangeID,
	)
	if failure.ProviderStatus != 0 {
		message += fmt.Sprintf(" providerStatus=%d", failure.ProviderStatus)
	}
	if failure.ProviderField != ProviderFieldUnknown {
		message += fmt.Sprintf(" providerField=%s", failure.ProviderField)
	}
	if failure.ClientField != ClientFieldUnknown {
		message += fmt.Sprintf(" clientField=%s", failure.ClientField)
	}
	if failure.ProtocolReason != "" {
		message += fmt.Sprintf(" protocolReason=%s", failure.ProtocolReason)
	}
	if failure.ResponseIssue != "" {
		message += fmt.Sprintf(" providerResponseIssue=%s", failure.ResponseIssue)
	}
	if failure.cause != nil {
		message += ": " + failure.cause.Error()
	}
	return message
}

func newProviderStatusFailure(
	exchangeID string,
	providerStatus int,
	providerField ProviderField,
) *Failure {
	return &Failure{
		Code:           ReasonProviderStatusRejected,
		ExchangeID:     exchangeID,
		ProviderStatus: providerStatus,
		ProviderField:  providerField,
		cause:          errors.New("provider returned a non-success status"),
	}
}

func (failure *Failure) Unwrap() error {
	if failure == nil {
		return nil
	}
	return failure.cause
}

func newFailure(
	code ReasonCode,
	exchangeID string,
	providerStatus int,
	cause error,
) *Failure {
	if cause == nil {
		cause = errors.New("Exchange operation failed")
	}
	return &Failure{
		Code:           code,
		ExchangeID:     exchangeID,
		ProviderStatus: providerStatus,
		ProtocolReason: protocolcore.ReasonOf(cause),
		cause:          cause,
	}
}

func ReasonOf(err error) ReasonCode {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

type ReplayClass string

const (
	ReplaySafe               ReplayClass = "safe"
	ReplayIdempotencyKeyed   ReplayClass = "idempotency_keyed"
	ReplayGenerationCostOnly ReplayClass = "generation_cost_only"
	ReplaySideEffectPossible ReplayClass = "side_effect_possible"
	ReplayNonReplayable      ReplayClass = "non_replayable"
	ReplayUnknown            ReplayClass = "unknown"
)

func (class ReplayClass) validate() error {
	switch class {
	case ReplaySafe,
		ReplayIdempotencyKeyed,
		ReplayGenerationCostOnly,
		ReplaySideEffectPossible,
		ReplayNonReplayable,
		ReplayUnknown:
		return nil
	default:
		return errors.New("replay class is invalid")
	}
}

func (class ReplayClass) allowsTransportResend() bool {
	switch class {
	case ReplaySafe, ReplayIdempotencyKeyed, ReplayGenerationCostOnly:
		return true
	default:
		return false
	}
}

// ClientRequest is an owned immutable ingress representation. A future ingress
// adapter constructs it once after selecting the Access identity.
type ClientRequest struct {
	exchangeID     string
	ingress        access.IngressBinding
	body           []byte
	replayClass    ReplayClass
	clientHello    transportprofile.Observation
	hasClientHello bool
}

type clientRequestOptionKind uint8

const clientRequestOptionClientHello clientRequestOptionKind = 1

// ClientRequestOption is a closed typed option. Its fields are private so an
// ingress adapter cannot create an unvalidated option shape.
type ClientRequestOption struct {
	kind        clientRequestOptionKind
	clientHello transportprofile.Observation
}

func WithClientHelloObservation(
	observation transportprofile.Observation,
) ClientRequestOption {
	return ClientRequestOption{
		kind:        clientRequestOptionClientHello,
		clientHello: observation,
	}
}

func NewClientRequest(
	exchangeID string,
	ingress access.IngressBinding,
	body []byte,
	replayClass ReplayClass,
	options ...ClientRequestOption,
) (ClientRequest, error) {
	if err := validateIdentity("Exchange ID", exchangeID); err != nil {
		return ClientRequest{}, err
	}
	if err := ingress.Validate(); err != nil {
		return ClientRequest{}, err
	}
	if len(body) == 0 || len(body) > providertransport.MaxProviderRequestBytes {
		return ClientRequest{}, errors.New("client request body has an invalid size")
	}
	if err := replayClass.validate(); err != nil {
		return ClientRequest{}, err
	}
	request := ClientRequest{
		exchangeID:  exchangeID,
		ingress:     ingress,
		body:        bytes.Clone(body),
		replayClass: replayClass,
	}
	for _, option := range options {
		switch option.kind {
		case clientRequestOptionClientHello:
			if request.hasClientHello {
				return ClientRequest{}, errors.New(
					"client TLS ClientHello option is duplicated",
				)
			}
			if !option.clientHello.Available() {
				return ClientRequest{}, errors.New(
					"client TLS ClientHello observation is unavailable",
				)
			}
			request.clientHello = option.clientHello
			request.hasClientHello = true
		default:
			return ClientRequest{}, errors.New(
				"client request option is invalid",
			)
		}
	}
	return request, nil
}

func (request ClientRequest) ExchangeID() string {
	return request.exchangeID
}

func (request ClientRequest) AccessID() access.AccessID {
	return request.ingress.AccessID()
}

func (request ClientRequest) IngressBinding() access.IngressBinding {
	return request.ingress
}

func (request ClientRequest) Body() []byte {
	return bytes.Clone(request.body)
}

func (request ClientRequest) ReplayClass() ReplayClass {
	return request.replayClass
}

func (request ClientRequest) ClientHelloObservation() (
	transportprofile.Observation,
	bool,
) {
	return request.clientHello, request.hasClientHello
}

func (request ClientRequest) validate() error {
	if err := validateIdentity("Exchange ID", request.exchangeID); err != nil {
		return err
	}
	if err := request.ingress.Validate(); err != nil {
		return err
	}
	if len(request.body) == 0 ||
		len(request.body) > providertransport.MaxProviderRequestBytes {
		return errors.New("client request body has an invalid size")
	}
	if request.hasClientHello && !request.clientHello.Available() {
		return errors.New("client TLS ClientHello observation is unavailable")
	}
	return request.replayClass.validate()
}

type ResponseMode string

const (
	ResponseModeEventStream ResponseMode = "event_stream"
	ResponseModeJSON        ResponseMode = "json"
)

// FailureNotice is the in-band streaming failure contract. It intentionally
// contains no localized or provider-supplied text.
type FailureNotice struct {
	ReasonCode     ReasonCode
	ProviderStatus int
	ProviderField  ProviderField
	ProtocolReason protocolcore.Reason
	ResponseIssue  ProviderResponseIssue
}

// Downstream is implemented by a future ingress adapter. Begin commits the
// response envelope, Write reports exact committed bytes, and Abort emits the
// stable in-band stream failure representation.
type Downstream interface {
	Begin(context.Context, ResponseMode) error
	Write(context.Context, []byte) (int, error)
	Keepalive(context.Context) error
	Abort(context.Context, FailureNotice) error
}

type Provider interface {
	Do(
		context.Context,
		providertransport.Request,
	) (*http.Response, providertransport.Evidence, error)
}

type AttemptObservation struct {
	ExchangeID     string
	AccessID       access.AccessID
	Outcome        AttemptOutcome
	ReasonCode     ReasonCode
	ProviderStatus int
	ProviderField  ProviderField
	ClientField    ClientField
	Transport      transportprofile.Evidence
}

type AttemptObserver interface {
	Observe(context.Context, AttemptObservation) error
}

type ToolDecisionOutcome string

const (
	ToolDecisionApproved ToolDecisionOutcome = "approved"
	ToolDecisionRejected ToolDecisionOutcome = "rejected"
)

type ToolDecision struct {
	Outcome    ToolDecisionOutcome
	ReasonCode string
}

func (decision ToolDecision) validate() error {
	switch decision.Outcome {
	case ToolDecisionApproved:
		if decision.ReasonCode != "" {
			return errors.New("approved tool decision contains a reason code")
		}
	case ToolDecisionRejected:
		if err := validateIdentity("tool decision reason code", decision.ReasonCode); err != nil {
			return err
		}
	default:
		return errors.New("tool decision outcome is invalid")
	}
	return nil
}

// ToolDecisionRequest is an immutable complete decision group. Parallel tool
// calls from one provider terminal are never split into independent decisions.
type ToolDecisionRequest struct {
	exchangeID   string
	accessID     access.AccessID
	planRevision access.Revision
	planHash     access.PlanHash
	intents      []protocolcore.ToolIntent
}

func NewToolDecisionRequest(
	exchangeID string,
	accessID access.AccessID,
	planRevision access.Revision,
	planHash access.PlanHash,
	intents []protocolcore.ToolIntent,
) (ToolDecisionRequest, error) {
	if err := validateIdentity("Exchange ID", exchangeID); err != nil ||
		accessID.String() == "" ||
		planRevision == 0 ||
		planHash.IsZero() ||
		len(intents) == 0 ||
		len(intents) > protocolcore.MaxToolCount {
		return ToolDecisionRequest{}, errors.New("tool decision request is invalid")
	}
	for _, intent := range intents {
		if err := intent.Validate(); err != nil {
			return ToolDecisionRequest{}, err
		}
	}
	return ToolDecisionRequest{
		exchangeID:   exchangeID,
		accessID:     accessID,
		planRevision: planRevision,
		planHash:     planHash,
		intents:      cloneToolIntents(intents),
	}, nil
}

func (request ToolDecisionRequest) ExchangeID() string {
	return request.exchangeID
}

func (request ToolDecisionRequest) AccessID() access.AccessID {
	return request.accessID
}

func (request ToolDecisionRequest) PlanRevision() access.Revision {
	return request.planRevision
}

func (request ToolDecisionRequest) PlanHash() access.PlanHash {
	return request.planHash
}

func (request ToolDecisionRequest) ToolIntents() []protocolcore.ToolIntent {
	return cloneToolIntents(request.intents)
}

type ToolDecisionGate interface {
	Decide(context.Context, ToolDecisionRequest) (ToolDecision, error)
}

// RejectAllToolDecisions is an explicit fail-closed policy for hosts that have
// not composed an approval authority. It is production behavior, not a test
// approval substitute.
type RejectAllToolDecisions struct {
	reasonCode string
}

func NewRejectAllToolDecisions(reasonCode string) (RejectAllToolDecisions, error) {
	if err := validateIdentity("tool rejection reason code", reasonCode); err != nil {
		return RejectAllToolDecisions{}, err
	}
	return RejectAllToolDecisions{reasonCode: reasonCode}, nil
}

func (policy RejectAllToolDecisions) Decide(
	ctx context.Context,
	_ ToolDecisionRequest,
) (ToolDecision, error) {
	if ctx == nil {
		return ToolDecision{}, errors.New("tool decision context is nil")
	}
	if err := ctx.Err(); err != nil {
		return ToolDecision{}, err
	}
	return ToolDecision{
		Outcome:    ToolDecisionRejected,
		ReasonCode: policy.reasonCode,
	}, nil
}

type RetryObservation struct {
	ExchangeID    string
	ResendOrdinal uint32
	StatusCode    int
	Backoff       time.Duration
}

type RetryWaiter interface {
	WaitForRetry(context.Context, RetryObservation) error
}

// TimerRetryWaiter is the bounded production fallback when no native network
// change signal is composed. A timer is not treated as reachability evidence.
type TimerRetryWaiter struct{}

func (TimerRetryWaiter) WaitForRetry(
	ctx context.Context,
	observation RetryObservation,
) error {
	if ctx == nil {
		return errors.New("transport retry context is nil")
	}
	if observation.Backoff < 0 {
		return errors.New("transport retry backoff is negative")
	}
	timer := time.NewTimer(observation.Backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

type HoldPolicy struct {
	MaxTransportResends uint32
	RetryDelay          time.Duration
	MaxDuration         time.Duration
}

func DefaultHoldPolicy() HoldPolicy {
	return HoldPolicy{
		MaxTransportResends: 1,
		RetryDelay:          time.Second,
		MaxDuration:         30 * time.Second,
	}
}

func (policy HoldPolicy) Validate() error {
	if policy.MaxTransportResends > 16 {
		return errors.New("transport resend limit exceeds the supported bound")
	}
	if policy.RetryDelay < 0 {
		return errors.New("transport retry delay is negative")
	}
	if policy.MaxDuration <= 0 {
		return errors.New("transport retry duration must be positive")
	}
	return nil
}

type StreamBudgets struct {
	KeepaliveInterval       time.Duration
	ProviderProgressTimeout time.Duration
}

func DefaultStreamBudgets() StreamBudgets {
	return StreamBudgets{
		KeepaliveInterval:       15 * time.Second,
		ProviderProgressTimeout: 60 * time.Second,
	}
}

func (budgets StreamBudgets) Validate() error {
	if budgets.KeepaliveInterval <= 0 ||
		budgets.ProviderProgressTimeout <= 0 {
		return errors.New("stream progress budgets must be positive")
	}
	if budgets.KeepaliveInterval >= budgets.ProviderProgressTimeout {
		return errors.New(
			"downstream keepalive interval must be shorter than provider progress timeout",
		)
	}
	return nil
}

type Options struct {
	OwnerContext       context.Context
	Actions            offlinehold.ActionAdmission
	Resolver           access.SnapshotResolver
	ProtocolPath       *protocolpath.Path
	Provider           Provider
	ToolDecisions      ToolDecisionGate
	RetryWaiter        RetryWaiter
	Observer           AttemptObserver
	ObservationTimeout time.Duration
	Hold               HoldPolicy
	Stream             StreamBudgets
}

type AttemptOutcome string

const (
	AttemptSucceeded AttemptOutcome = "succeeded"
	AttemptFailed    AttemptOutcome = "failed"
	AttemptCanceled  AttemptOutcome = "canceled"
	AttemptAborted   AttemptOutcome = "aborted"
)

// Result is evidence for one logical Exchange and its one M0 Attempt. It does
// not expose the Access plan handle or provider response body.
type Result struct {
	ExchangeID          string
	AccessID            string
	AccessRevision      access.Revision
	PlanHash            string
	RouteHost           string
	CredentialBindingID string
	Outcome             AttemptOutcome
	TransportResends    uint32
	Ledger              LedgerSnapshot
	Translation         protocolcore.TranslationReport
	Credential          providertransport.CredentialEvidence
	Transport           transportprofile.Evidence
}

func cloneToolIntents(
	intents []protocolcore.ToolIntent,
) []protocolcore.ToolIntent {
	cloned := make([]protocolcore.ToolIntent, len(intents))
	for index, intent := range intents {
		cloned[index] = intent.Clone()
	}
	return cloned
}

func validateIdentity(label, value string) error {
	if value == "" ||
		len(value) > MaxExchangeIdentityBytes ||
		!utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%s is invalid", label)
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("%s contains a control character", label)
		}
	}
	return nil
}

func cloneStrings(values []string) []string {
	return slices.Clone(values)
}
