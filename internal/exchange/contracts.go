// Package exchange executes one immutable Environment request plan against the
// protocol and provider boundaries. It owns Exchange admission, Attempt commit
// accounting, and downstream publication, but it does not own an ingress
// listener or resolve mutable configuration.
package exchange

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/captureadmission"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/transportprofile"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

const MaxExchangeIdentityBytes = 512

type ReasonCode string

const (
	ReasonInvalidExchangeRequest        ReasonCode = "invalid_exchange_request"
	ReasonUnsupportedClientInput        ReasonCode = "unsupported_client_input"
	ReasonEnvironmentPlanInvalid        ReasonCode = "environment_plan_invalid"
	ReasonOfflineHoldUnavailable        ReasonCode = "offline_hold_unavailable"
	ReasonProviderRequestInvalid        ReasonCode = "provider_request_invalid"
	ReasonMessageTransformFailed        ReasonCode = "message_transform_failed"
	ReasonProviderCredentialUnavailable ReasonCode = "provider_credential_unavailable"
	ReasonProviderTransportFailed       ReasonCode = "provider_transport_failed"
	ReasonProviderResponseIdle          ReasonCode = "provider_response_idle"
	ReasonProviderStatusRejected        ReasonCode = "provider_status_rejected"
	ReasonProviderResponseInvalid       ReasonCode = "provider_response_invalid"
	ReasonTransportRetryExhausted       ReasonCode = "transport_retry_exhausted"
	ReasonToolDecisionRejected          ReasonCode = "tool_decision_rejected"
	ReasonToolDecisionExpired           ReasonCode = "tool_decision_expired"
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
	// ClientPath is where in the request's shape the failure happened: field
	// names and indices, never a value. It is what makes a rejected request
	// diagnosable without rebuilding the runtime.
	ClientPath     string
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
	failure := &Failure{
		Code:           code,
		ExchangeID:     exchangeID,
		ProviderStatus: providerStatus,
		ClientPath:     structuralPath(cause),
		ProtocolReason: protocolcore.ReasonOf(cause),
		cause:          cause,
	}
	return failure
}

// structuralPath extracts the failure's location in the request's shape.
//
// It is deliberately strict about what a path may look like. A path is a
// sequence of field names and indices; anything else is a string that came
// from somewhere else, and a diagnostic that leaks content is worse than no
// diagnostic at all.
func structuralPath(cause error) string {
	var failure *protocolcore.Failure
	if !errors.As(cause, &failure) || failure.Path == "" {
		return ""
	}
	if len(failure.Path) > MaxClientPathBytes ||
		!utf8.ValidString(failure.Path) ||
		failure.Path[0] != '$' {
		return ""
	}
	for _, character := range failure.Path {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '$',
			character == '.',
			character == '_',
			character == '-',
			character == '[',
			character == ']':
		default:
			return ""
		}
	}
	return failure.Path
}

// MaxClientPathBytes bounds the structural path.
const MaxClientPathBytes = 256

// ClientPathOf reports where in the request's shape a failure happened.
func ClientPathOf(err error) string {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.ClientPath
	}
	return ""
}

func ReasonOf(err error) ReasonCode {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

// ProviderStatusOf returns the upstream HTTP status recorded on a classified
// Exchange failure. It exposes only the numeric status needed to preserve the
// provider's retry semantics at the downstream HTTP boundary; response bodies
// and private transport causes remain internal evidence.
func ProviderStatusOf(err error) int {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure.ProviderStatus
	}
	return 0
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

// ClientOperationEvidence freezes the operation-catalog match made before
// semantic decoding. Exchange revalidates it against the one resolved plan.
type ClientOperationEvidence struct {
	id       protocolspec.ClientOperationID
	revision protocolspec.Revision
	method   string
	path     string
	rawQuery string
}

func NewClientOperationEvidence(
	id protocolspec.ClientOperationID,
	revision protocolspec.Revision,
	method string,
	path string,
	rawQuery string,
) (ClientOperationEvidence, error) {
	evidence := ClientOperationEvidence{
		id:       id,
		revision: revision,
		method:   method,
		path:     path,
		rawQuery: rawQuery,
	}
	if err := evidence.validate(); err != nil {
		return ClientOperationEvidence{}, err
	}
	return evidence, nil
}

func (evidence ClientOperationEvidence) ID() protocolspec.ClientOperationID {
	return evidence.id
}

func (evidence ClientOperationEvidence) Revision() protocolspec.Revision {
	return evidence.revision
}

func (evidence ClientOperationEvidence) Method() string {
	return evidence.method
}

func (evidence ClientOperationEvidence) Path() string {
	return evidence.path
}

func (evidence ClientOperationEvidence) RawQuery() string {
	return evidence.rawQuery
}

func (evidence ClientOperationEvidence) validate() error {
	if evidence.id.String() == "" ||
		evidence.revision == 0 ||
		!validOperationMethod(evidence.method) ||
		!canonicalOperationPath(evidence.path) ||
		(evidence.rawQuery != "" &&
			!canonicalOperationQuery(evidence.rawQuery)) {
		return errors.New("client operation evidence is invalid")
	}
	return nil
}

// ClientRequest is an owned immutable ingress representation.
type ClientRequest struct {
	exchangeID         string
	plan               environment.RequestPlan
	operation          ClientOperationEvidence
	body               []byte
	replayClass        ReplayClass
	clientProtocol     wireprofile.ApplicationProtocol
	clientHello        transportprofile.Observation
	hasClientHello     bool
	admission          captureadmission.Admission
	connectionRef      string
	hasCorrelation     bool
	anthropicBeta      string
	clientUserAgent    string
	clientEvidence     []protocolcore.ProtocolEvidenceValue
	hasClientEvidence  bool
	originalHeaders    http.Header
	hasOriginalHeaders bool
}

type clientRequestOptionKind uint8

const (
	clientRequestOptionClientHello      clientRequestOptionKind = 1
	clientRequestOptionCorrelation      clientRequestOptionKind = 2
	clientRequestOptionAnthropicBeta    clientRequestOptionKind = 3
	clientRequestOptionUserAgent        clientRequestOptionKind = 4
	clientRequestOptionOriginalHeaders  clientRequestOptionKind = 5
	clientRequestOptionProtocolEvidence clientRequestOptionKind = 6
)

// ClientRequestOption is a closed typed option. Its fields are private so an
// ingress adapter cannot create an unvalidated option shape.
type ClientRequestOption struct {
	kind            clientRequestOptionKind
	clientHello     transportprofile.Observation
	admission       captureadmission.Admission
	connectionRef   string
	anthropicBeta   string
	clientUserAgent string
	clientEvidence  []protocolcore.ProtocolEvidenceValue
	originalHeaders http.Header
}

func WithClientHelloObservation(
	observation transportprofile.Observation,
) ClientRequestOption {
	return ClientRequestOption{
		kind:        clientRequestOptionClientHello,
		clientHello: observation,
	}
}

// WithIngressCorrelation associates this Exchange with the route-neutral
// capture admission and client connection it entered through. ADR-0015
// section 10 forbids encoding containment in an identity string, so every
// identity is generated independently and association travels as typed data.
func WithIngressCorrelation(
	admission captureadmission.Admission,
	connectionRef string,
) ClientRequestOption {
	return ClientRequestOption{
		kind:          clientRequestOptionCorrelation,
		admission:     admission,
		connectionRef: connectionRef,
	}
}

// WithAnthropicBetaHeader preserves only Anthropic's public feature selector.
// Credentials and arbitrary client headers never enter the Exchange request.
func WithAnthropicBetaHeader(value string) ClientRequestOption {
	return ClientRequestOption{
		kind:          clientRequestOptionAnthropicBeta,
		anthropicBeta: value,
	}
}

// WithClientUserAgent carries one bounded semantic identity field into the
// presentation boundary. No other arbitrary client header crosses Exchange.
func WithClientUserAgent(value string) ClientRequestOption {
	return ClientRequestOption{
		kind:            clientRequestOptionUserAgent,
		clientUserAgent: value,
	}
}

// WithClientProtocolEvidence carries a bounded allowlist of non-secret,
// client-native identifiers across the ingress boundary. Raw HTTP remains the
// wire authority; these canonical values exist only so semantic projections
// can associate Exchanges without guessing from time, titles, or content.
func WithClientProtocolEvidence(
	values []protocolcore.ProtocolEvidenceValue,
) ClientRequestOption {
	return ClientRequestOption{
		kind:           clientRequestOptionProtocolEvidence,
		clientEvidence: slices.Clone(values),
	}
}

// WithOriginalHeaders carries the client-owned request envelope needed only
// by an exact client-passthrough route. A managed-credential path never reads
// it; the frozen Environment plan must prove exact ClientOrigin equality before these headers can
// reach a transport. Values remain in memory only and are never evidence.
func WithOriginalHeaders(headers http.Header) ClientRequestOption {
	return ClientRequestOption{
		kind:            clientRequestOptionOriginalHeaders,
		originalHeaders: headers.Clone(),
	}
}

func NewClientRequest(
	exchangeID string,
	plan environment.RequestPlan,
	operation ClientOperationEvidence,
	body []byte,
	replayClass ReplayClass,
	clientProtocol wireprofile.ApplicationProtocol,
	options ...ClientRequestOption,
) (ClientRequest, error) {
	if err := validateIdentity("Exchange ID", exchangeID); err != nil {
		return ClientRequest{}, err
	}
	if err := validateFrozenRequestPlan(plan); err != nil {
		return ClientRequest{}, err
	}
	if err := operation.validate(); err != nil {
		return ClientRequest{}, err
	}
	if len(body) == 0 || len(body) > providertransport.MaxProviderRequestBytes {
		return ClientRequest{}, errors.New("client request body has an invalid size")
	}
	if err := replayClass.validate(); err != nil {
		return ClientRequest{}, err
	}
	if !clientProtocol.Valid() {
		return ClientRequest{}, errors.New("client HTTP protocol is unavailable")
	}
	if err := validateFrozenWireVariant(plan, clientProtocol); err != nil {
		return ClientRequest{}, err
	}
	request := ClientRequest{
		exchangeID:     exchangeID,
		plan:           plan,
		operation:      operation,
		body:           bytes.Clone(body),
		replayClass:    replayClass,
		clientProtocol: clientProtocol,
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
		case clientRequestOptionCorrelation:
			if request.hasCorrelation {
				return ClientRequest{}, errors.New(
					"ingress correlation option is duplicated",
				)
			}
			if err := option.admission.Validate(); err != nil {
				return ClientRequest{}, err
			}
			if err := validateIdentity(
				"connection reference",
				option.connectionRef,
			); err != nil {
				return ClientRequest{}, err
			}
			request.admission = option.admission
			request.connectionRef = option.connectionRef
			request.hasCorrelation = true
		case clientRequestOptionAnthropicBeta:
			if request.anthropicBeta != "" ||
				!validAnthropicBetaHeader(option.anthropicBeta) {
				return ClientRequest{}, errors.New(
					"Anthropic beta header option is invalid",
				)
			}
			request.anthropicBeta = option.anthropicBeta
		case clientRequestOptionUserAgent:
			if request.clientUserAgent != "" ||
				!validClientUserAgent(option.clientUserAgent) {
				return ClientRequest{}, errors.New(
					"client User-Agent option is invalid",
				)
			}
			request.clientUserAgent = option.clientUserAgent
		case clientRequestOptionProtocolEvidence:
			if request.hasClientEvidence || len(option.clientEvidence) == 0 {
				return ClientRequest{}, errors.New(
					"client protocol evidence option is invalid",
				)
			}
			if err := protocolcore.ValidateProtocolEvidence(
				option.clientEvidence,
			); err != nil {
				return ClientRequest{}, err
			}
			request.clientEvidence = slices.Clone(option.clientEvidence)
			request.hasClientEvidence = true
		case clientRequestOptionOriginalHeaders:
			if request.hasOriginalHeaders {
				return ClientRequest{}, errors.New(
					"original request header option is duplicated",
				)
			}
			originalHeaders, err := validateOriginalHeaders(
				option.originalHeaders,
			)
			if err != nil {
				return ClientRequest{}, err
			}
			request.originalHeaders = originalHeaders
			request.hasOriginalHeaders = true
		default:
			return ClientRequest{}, errors.New(
				"client request option is invalid",
			)
		}
	}
	return request, nil
}

func validAnthropicBetaHeader(value string) bool {
	if value == "" || len(value) > 4096 || !utf8.ValidString(value) {
		return false
	}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return false
		}
		for _, character := range item {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') ||
				character == '-' || character == '_' || character == '.' {
				continue
			}
			return false
		}
	}
	return true
}

func (request ClientRequest) protocolHeaders() http.Header {
	headers := make(http.Header)
	if request.anthropicBeta != "" {
		headers.Set("Anthropic-Beta", request.anthropicBeta)
	}
	return headers
}

func (request ClientRequest) ClientUserAgent() string {
	return request.clientUserAgent
}

func (request ClientRequest) ClientProtocolEvidence() []protocolcore.ProtocolEvidenceValue {
	return slices.Clone(request.clientEvidence)
}

func (request ClientRequest) OriginalHeaders() (http.Header, bool) {
	return request.originalHeaders.Clone(), request.hasOriginalHeaders
}

func validateOriginalHeaders(source http.Header) (http.Header, error) {
	headers := source.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Del("Proxy-Authorization")
	headers.Del("Proxy-Connection")
	total := 0
	for name, values := range headers {
		if !validHTTPHeaderName(name) {
			return nil, errors.New("original request header name is invalid")
		}
		total += len(name)
		for _, value := range values {
			if strings.ContainsAny(value, "\r\n") || !utf8.ValidString(value) {
				return nil, errors.New("original request header value is invalid")
			}
			total += len(value)
		}
		if total > 64<<10 {
			return nil, errors.New("original request headers exceed the size limit")
		}
	}
	return headers, nil
}

func validHTTPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') {
			continue
		}
		switch character {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func validClientUserAgent(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

// CaptureAdmission is the authenticated, route-neutral ingress evidence for
// this Exchange, or absent for a runtime-originated Exchange.
func (request ClientRequest) CaptureAdmission() (
	captureadmission.Admission,
	bool,
) {
	return request.admission, request.hasCorrelation
}

func (request ClientRequest) CaptureAdmissionRef() string {
	if !request.hasCorrelation {
		return ""
	}
	return request.admission.AdmissionRef()
}

// CaptureRunRef is present only for an Exchange admitted through managed run.
func (request ClientRequest) CaptureRunRef() string {
	value, _ := request.admission.CaptureRunID()
	return value
}

// ManualCaptureRef is present only for an Exchange admitted through a manual
// capture grant.
func (request ClientRequest) ManualCaptureRef() string {
	value, _ := request.admission.ManualCaptureID()
	return value
}

// ConnectionRef is the client connection this Exchange entered through, or
// empty for a runtime-originated Exchange.
func (request ClientRequest) ConnectionRef() string {
	return request.connectionRef
}

func (request ClientRequest) WorkspaceScope() (workspaceidentity.Scope, bool) {
	if !request.hasCorrelation {
		return workspaceidentity.Scope{}, false
	}
	return request.admission.WorkspaceScope()
}

func (request ClientRequest) ExchangeID() string {
	return request.exchangeID
}

func (request ClientRequest) RequestPlan() environment.RequestPlan {
	return request.plan
}

func (request ClientRequest) ClientOperation() ClientOperationEvidence {
	return request.operation
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

func (request ClientRequest) ClientHTTPProtocol() wireprofile.ApplicationProtocol {
	return request.clientProtocol
}

func (request ClientRequest) validate() error {
	if err := validateIdentity("Exchange ID", request.exchangeID); err != nil {
		return err
	}
	if err := validateFrozenRequestPlan(request.plan); err != nil {
		return err
	}
	if err := request.operation.validate(); err != nil {
		return err
	}
	if len(request.body) == 0 ||
		len(request.body) > providertransport.MaxProviderRequestBytes {
		return errors.New("client request body has an invalid size")
	}
	if request.hasClientHello && !request.clientHello.Available() {
		return errors.New("client TLS ClientHello observation is unavailable")
	}
	if !request.clientProtocol.Valid() {
		return errors.New("client HTTP protocol is unavailable")
	}
	if err := validateFrozenWireVariant(request.plan, request.clientProtocol); err != nil {
		return err
	}
	if request.hasOriginalHeaders {
		if _, err := validateOriginalHeaders(request.originalHeaders); err != nil {
			return err
		}
	}
	if request.hasClientEvidence {
		if len(request.clientEvidence) == 0 {
			return errors.New("client protocol evidence is unavailable")
		}
		if err := protocolcore.ValidateProtocolEvidence(request.clientEvidence); err != nil {
			return err
		}
	}
	return request.replayClass.validate()
}

func validOperationMethod(value string) bool {
	if value == "" || strings.ToUpper(value) != value {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') &&
			character != '-' {
			return false
		}
	}
	return true
}

func canonicalOperationPath(value string) bool {
	if value == "" ||
		len(value) > protocolspec.MaxOperationPath ||
		!utf8.ValidString(value) ||
		value[0] != '/' ||
		strings.ContainsAny(value, "\\%\x00\r\n\t") ||
		pathpkg.Clean(value) != value ||
		(value != "/" && strings.HasSuffix(value, "/")) {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func canonicalOperationQuery(value string) bool {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.ParseQuery(value)
	return err == nil && parsed.Encode() == value
}

type ResponseMode string

const (
	ResponseModeEventStream ResponseMode = "event_stream"
	ResponseModeJSON        ResponseMode = "json"
)

// ResponseEnvelope is the immutable downstream status and header boundary.
// It contains no body bytes. Hop-by-hop headers are removed at construction;
// original passthrough may preserve end-to-end provider headers without giving
// the transport control over the client connection.
type ResponseEnvelope struct {
	mode       ResponseMode
	statusCode int
	headers    http.Header
}

func NewResponseEnvelope(
	mode ResponseMode,
	statusCode int,
	headers http.Header,
) (ResponseEnvelope, error) {
	if mode != ResponseModeJSON && mode != ResponseModeEventStream {
		return ResponseEnvelope{}, errors.New("downstream response mode is invalid")
	}
	if statusCode < 200 || statusCode > 599 {
		return ResponseEnvelope{}, errors.New("downstream status code is invalid")
	}
	clean := headers.Clone()
	if clean == nil {
		clean = make(http.Header)
	}
	for _, token := range strings.Split(clean.Get("Connection"), ",") {
		clean.Del(strings.TrimSpace(token))
	}
	for _, name := range []string{
		"Connection",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Keep-Alive",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	} {
		clean.Del(name)
	}
	return ResponseEnvelope{
		mode:       mode,
		statusCode: statusCode,
		headers:    clean,
	}, nil
}

func (envelope ResponseEnvelope) Mode() ResponseMode { return envelope.mode }
func (envelope ResponseEnvelope) StatusCode() int    { return envelope.statusCode }
func (envelope ResponseEnvelope) Headers() http.Header {
	return envelope.headers.Clone()
}

func managedResponseEnvelope(mode ResponseMode) ResponseEnvelope {
	contentType := "application/json"
	if mode == ResponseModeEventStream {
		contentType = "text/event-stream"
	}
	return ResponseEnvelope{
		mode:       mode,
		statusCode: http.StatusOK,
		headers: http.Header{
			"Cache-Control": []string{"no-store"},
			"Content-Type":  []string{contentType},
		},
	}
}

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
	Begin(context.Context, ResponseEnvelope) error
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

// AccountLeaseRequest is the immutable authorization scope for one managed
// provider attempt. It is derived only from the frozen Environment request
// plan; an ingress caller cannot choose a different account or realm.
type AccountLeaseRequest struct {
	environmentID            environment.EnvironmentID
	environmentRevision      environment.Revision
	environmentDigest        environment.CandidateDigest
	routeID                  environment.UpstreamRouteID
	routeRevision            environment.Revision
	upstreamEndpointID       string
	upstreamEndpointRevision environment.Revision
	accountID                string
	accountRevision          environment.Revision
	realmID                  string
}

func (request AccountLeaseRequest) EnvironmentID() environment.EnvironmentID {
	return request.environmentID
}

func (request AccountLeaseRequest) EnvironmentRevision() environment.Revision {
	return request.environmentRevision
}

func (request AccountLeaseRequest) EnvironmentDigest() environment.CandidateDigest {
	return request.environmentDigest
}

func (request AccountLeaseRequest) RouteID() environment.UpstreamRouteID {
	return request.routeID
}

func (request AccountLeaseRequest) RouteRevision() environment.Revision {
	return request.routeRevision
}

func (request AccountLeaseRequest) UpstreamEndpointID() string {
	return request.upstreamEndpointID
}

func (request AccountLeaseRequest) UpstreamEndpointRevision() environment.Revision {
	return request.upstreamEndpointRevision
}

func (request AccountLeaseRequest) AccountID() string {
	return request.accountID
}

func (request AccountLeaseRequest) AccountRevision() environment.Revision {
	return request.accountRevision
}

func (request AccountLeaseRequest) RealmID() string {
	return request.realmID
}

type AccountLeaseAuthority interface {
	Acquire(context.Context, AccountLeaseRequest) (providerauth.Lease, error)
}

type AttemptObservation struct {
	ExchangeID             string
	EnvironmentID          environment.EnvironmentID
	EnvironmentRevision    environment.Revision
	EnvironmentDigest      string
	EndpointID             environment.ClientEndpointID
	EndpointRevision       environment.Revision
	ProtocolPlanID         environment.ClientProtocolPlanID
	ProtocolPlanRevision   environment.Revision
	RouteID                environment.UpstreamRouteID
	RouteRevision          environment.Revision
	AccountID              string
	AccountRevision        uint64
	CredentialEpoch        uint64
	Admission              captureadmission.Admission
	HasAdmission           bool
	ConnectionID           string
	Outcome                AttemptOutcome
	ReasonCode             ReasonCode
	ProviderStatus         int
	ProviderField          ProviderField
	ClientField            ClientField
	ClientPath             string
	Presentation           providertransport.WirePresentationEvidence
	Transport              transportprofile.Evidence
	Conversation           agentconversation.Ref
	ClientProtocolEvidence []protocolcore.ProtocolEvidenceValue
	ProviderResponseID     string
}

type StartObservation struct {
	ExchangeID             string
	EnvironmentID          environment.EnvironmentID
	EnvironmentRevision    environment.Revision
	EnvironmentDigest      string
	EndpointID             environment.ClientEndpointID
	EndpointRevision       environment.Revision
	ProtocolPlanID         environment.ClientProtocolPlanID
	ProtocolPlanRevision   environment.Revision
	RouteID                environment.UpstreamRouteID
	RouteRevision          environment.Revision
	Admission              captureadmission.Admission
	HasAdmission           bool
	ConnectionID           string
	Conversation           agentconversation.Ref
	ClientProtocolEvidence []protocolcore.ProtocolEvidenceValue
}

type ExchangeObserver interface {
	ObserveStart(context.Context, StartObservation) error
	ObserveTerminal(context.Context, AttemptObservation) error
}

// ContentObservation is the explicit plaintext boundary for one semantic
// Exchange. It is independent from AttemptObservation so body-free audit
// consumers cannot accidentally gain access to messages or tool arguments.
type ContentObservation struct {
	ExchangeID           string
	CaptureRunID         string
	ManualCaptureID      string
	EnvironmentID        environment.EnvironmentID
	EnvironmentRevision  environment.Revision
	EnvironmentDigest    string
	EndpointID           environment.ClientEndpointID
	EndpointRevision     environment.Revision
	ProtocolPlanID       environment.ClientProtocolPlanID
	ProtocolPlanRevision environment.Revision
	RouteID              environment.UpstreamRouteID
	RouteRevision        environment.Revision
	Recording            environment.ContentRecordingPolicy
	Request              protocolcore.Request
	Response             *protocolcore.Response
}

type ContentObserver interface {
	ObserveContent(context.Context, ContentObservation) error
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
	exchangeID          string
	environmentID       environment.EnvironmentID
	environmentRevision environment.Revision
	environmentDigest   environment.CandidateDigest
	routeID             environment.UpstreamRouteID
	routeRevision       environment.Revision
	decisionContext     ToolDecisionContext
	intents             []protocolcore.ToolIntent
}

// ToolDecisionContext freezes the policy and the only evidence allowed to
// classify structured workspace actions. A tool name on its own is never
// authority: Core also requires exact adapter capability, the request's tool
// schema, and the launcher's canonical workspace root.
type ToolDecisionContext struct {
	policySet                environment.PolicySet
	workspaceRoot            string
	structuredWorkspaceTools bool
	tools                    []protocolcore.ToolDefinition
	toolNamespaces           []protocolcore.ToolNamespace
}

func NewToolDecisionContext(
	policySet environment.PolicySet,
	workspaceRoot string,
	structuredWorkspaceTools bool,
	tools []protocolcore.ToolDefinition,
	toolNamespaces []protocolcore.ToolNamespace,
) (ToolDecisionContext, error) {
	if err := policySet.Validate(); err != nil {
		return ToolDecisionContext{}, err
	}
	if workspaceRoot != "" &&
		(!filepath.IsAbs(workspaceRoot) || filepath.Clean(workspaceRoot) != workspaceRoot) {
		return ToolDecisionContext{}, errors.New("tool decision workspace root is invalid")
	}
	if len(tools) > protocolcore.MaxToolCount ||
		len(toolNamespaces) > protocolcore.MaxToolCount {
		return ToolDecisionContext{}, errors.New("tool decision definitions exceed the limit")
	}
	toolNames := make(map[string]struct{}, len(tools))
	clonedTools := make([]protocolcore.ToolDefinition, len(tools))
	for index, tool := range tools {
		if err := tool.Validate(); err != nil {
			return ToolDecisionContext{}, err
		}
		if _, duplicate := toolNames[tool.Name]; duplicate {
			return ToolDecisionContext{}, errors.New("tool decision definition is duplicated")
		}
		toolNames[tool.Name] = struct{}{}
		clonedTools[index] = tool.Clone()
	}
	namespaceNames := make(map[string]struct{}, len(toolNamespaces))
	totalTools := len(tools)
	clonedNamespaces := make([]protocolcore.ToolNamespace, len(toolNamespaces))
	for index, namespace := range toolNamespaces {
		if err := namespace.Validate(); err != nil {
			return ToolDecisionContext{}, err
		}
		if _, duplicate := namespaceNames[namespace.Name]; duplicate {
			return ToolDecisionContext{}, errors.New("tool decision namespace is duplicated")
		}
		namespaceNames[namespace.Name] = struct{}{}
		totalTools += len(namespace.Tools)
		if totalTools > protocolcore.MaxToolCount {
			return ToolDecisionContext{}, errors.New("tool decision definitions exceed the total limit")
		}
		clonedNamespaces[index] = namespace.Clone()
	}
	return ToolDecisionContext{
		policySet: policySet, workspaceRoot: workspaceRoot,
		structuredWorkspaceTools: structuredWorkspaceTools,
		tools:                    clonedTools, toolNamespaces: clonedNamespaces,
	}, nil
}

func (context ToolDecisionContext) PolicySet() environment.PolicySet {
	return context.policySet
}

func (context ToolDecisionContext) WorkspaceRoot() (string, bool) {
	return context.workspaceRoot, context.workspaceRoot != ""
}

func (context ToolDecisionContext) StructuredWorkspaceTools() bool {
	return context.structuredWorkspaceTools
}

func (context ToolDecisionContext) Tools() []protocolcore.ToolDefinition {
	cloned := make([]protocolcore.ToolDefinition, len(context.tools))
	for index, tool := range context.tools {
		cloned[index] = tool.Clone()
	}
	return cloned
}

func (context ToolDecisionContext) ToolNamespaces() []protocolcore.ToolNamespace {
	cloned := make([]protocolcore.ToolNamespace, len(context.toolNamespaces))
	for index, namespace := range context.toolNamespaces {
		cloned[index] = namespace.Clone()
	}
	return cloned
}

func NewToolDecisionRequest(
	exchangeID string,
	environmentID environment.EnvironmentID,
	environmentRevision environment.Revision,
	environmentDigest environment.CandidateDigest,
	routeID environment.UpstreamRouteID,
	routeRevision environment.Revision,
	decisionContext ToolDecisionContext,
	intents []protocolcore.ToolIntent,
) (ToolDecisionRequest, error) {
	parsedEnvironmentID, environmentErr := environment.NewEnvironmentID(environmentID.String())
	parsedRouteID, routeErr := environment.NewUpstreamRouteID(routeID.String())
	parsedDigest, digestErr := environment.ParseCandidateDigest(environmentDigest.String())
	if err := validateIdentity("Exchange ID", exchangeID); err != nil ||
		environmentErr != nil || parsedEnvironmentID != environmentID ||
		routeErr != nil || parsedRouteID != routeID ||
		digestErr != nil || parsedDigest != environmentDigest ||
		environmentRevision == 0 || environmentRevision > environment.MaxRevision ||
		routeRevision == 0 || routeRevision > environment.MaxRevision ||
		decisionContext.policySet.Validate() != nil ||
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
		exchangeID: exchangeID, environmentID: environmentID,
		environmentRevision: environmentRevision,
		environmentDigest:   environmentDigest, routeID: routeID,
		routeRevision: routeRevision, decisionContext: decisionContext,
		intents: cloneToolIntents(intents),
	}, nil
}

func (request ToolDecisionRequest) ExchangeID() string {
	return request.exchangeID
}

func (request ToolDecisionRequest) EnvironmentID() environment.EnvironmentID {
	return request.environmentID
}

func (request ToolDecisionRequest) EnvironmentRevision() environment.Revision {
	return request.environmentRevision
}

func (request ToolDecisionRequest) EnvironmentDigest() environment.CandidateDigest {
	return request.environmentDigest
}

func (request ToolDecisionRequest) RouteID() environment.UpstreamRouteID {
	return request.routeID
}

func (request ToolDecisionRequest) RouteRevision() environment.Revision {
	return request.routeRevision
}

func (request ToolDecisionRequest) ToolIntents() []protocolcore.ToolIntent {
	return cloneToolIntents(request.intents)
}

func (request ToolDecisionRequest) Context() ToolDecisionContext {
	return request.decisionContext
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
	// AllowResendAfterProviderResponse permits resending a request the
	// provider already answered. Doing so may bill the user a second time and
	// may repeat an upstream side effect, so it defaults to refusing until an
	// attempt policy makes the decision explicit.
	AllowResendAfterProviderResponse bool
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
	Accounts           AccountLeaseAuthority
	ProtocolPaths      *protocolpath.Selector
	Provider           Provider
	ToolDecisions      ToolDecisionGate
	RetryWaiter        RetryWaiter
	Observer           ExchangeObserver
	ContentObserver    ContentObserver
	ObservationTimeout time.Duration
	Hold               HoldPolicy
	Stream             StreamBudgets
	AttemptIDs         AttemptIDSource
}

type AttemptOutcome string

const (
	AttemptSucceeded AttemptOutcome = "succeeded"
	AttemptFailed    AttemptOutcome = "failed"
	AttemptCanceled  AttemptOutcome = "canceled"
	AttemptAborted   AttemptOutcome = "aborted"
)

// Result is evidence for one logical Exchange and its provider attempts. It
// does not expose the Environment plan handle or provider response body.
type Result struct {
	ExchangeID           string
	EnvironmentID        string
	EnvironmentRevision  environment.Revision
	EnvironmentDigest    string
	EndpointID           string
	EndpointRevision     environment.Revision
	ProtocolPlanID       string
	ProtocolPlanRevision environment.Revision
	RouteID              string
	RouteRevision        environment.Revision
	RouteHost            string
	AccountID            string
	AccountRevision      uint64
	CredentialEpoch      uint64
	Outcome              AttemptOutcome
	TransportResends     uint32
	Ledger               LedgerSnapshot
	Translation          protocolcore.TranslationReport
	Credential           providertransport.CredentialEvidence
	Presentation         providertransport.WirePresentationEvidence
	Transport            transportprofile.Evidence
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
