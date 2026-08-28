package exchange

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/vibe-agi/vibermate/internal/agentconversation"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/clientannotation"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/messagetransform"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/providertransport"
	"github.com/vibe-agi/vibermate/internal/rawevidence"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const (
	streamReadBufferBytes    = 32 << 10
	maxCompleteResponseBytes = 16 << 20
	maxProviderErrorBytes    = 64 << 10
)

type operation struct {
	cancel     context.CancelCauseFunc
	stopClient func() bool
}

type contentCapture struct {
	request         *protocolcore.Request
	response        *protocolcore.Response
	requestObserved bool
}

var errOfflineHoldAdmission = errors.New(
	"offline-hold Exchange admission failed",
)

// Pipeline owns all active Exchange contexts. It has no listener and cannot be
// reached without an ingress component explicitly receiving its Executor.
type Pipeline struct {
	actions                  offlinehold.ActionAdmission
	accounts                 AccountLeaseAuthority
	protocolPaths            *protocolpath.Selector
	provider                 Provider
	toolDecisions            ToolDecisionGate
	retryWaiter              RetryWaiter
	observer                 ExchangeObserver
	content                  ContentObserver
	observeLimit             time.Duration
	hold                     HoldPolicy
	streamBudgets            StreamBudgets
	attemptIDs               AttemptIDSource
	annotations              *clientannotation.Signer
	now                      func() time.Time
	rawEvidence              rawevidence.Observer
	reportRawEvidenceFailure func(error)

	ownerContext context.Context
	cancelOwner  context.CancelCauseFunc

	mu         sync.Mutex
	closing    bool
	operations map[*operation]struct{}
	changed    chan struct{}
}

type Executor interface {
	Execute(context.Context, ClientRequest, Downstream) (Result, error)
}

var _ Executor = (*Pipeline)(nil)

func New(options Options) (*Pipeline, error) {
	if options.OwnerContext == nil ||
		options.Actions == nil ||
		options.ProtocolPaths == nil ||
		options.Provider == nil ||
		options.ToolDecisions == nil ||
		options.RetryWaiter == nil ||
		options.Observer == nil ||
		options.ContentObserver == nil ||
		options.ClientAnnotations == nil ||
		options.Now == nil ||
		options.ObservationTimeout <= 0 {
		return nil, errors.New("Exchange pipeline dependencies are incomplete")
	}
	if err := options.Hold.Validate(); err != nil {
		return nil, err
	}
	if err := options.Stream.Validate(); err != nil {
		return nil, err
	}
	attemptIDs := options.AttemptIDs
	if attemptIDs == nil {
		attemptIDs = NewCryptographicAttemptIDSource()
	}
	ownerContext, cancelOwner := context.WithCancelCause(options.OwnerContext)
	return &Pipeline{
		actions:                  options.Actions,
		accounts:                 options.Accounts,
		protocolPaths:            options.ProtocolPaths,
		provider:                 options.Provider,
		toolDecisions:            options.ToolDecisions,
		retryWaiter:              options.RetryWaiter,
		observer:                 options.Observer,
		content:                  options.ContentObserver,
		observeLimit:             options.ObservationTimeout,
		hold:                     options.Hold,
		streamBudgets:            options.Stream,
		attemptIDs:               attemptIDs,
		annotations:              options.ClientAnnotations,
		now:                      options.Now,
		rawEvidence:              options.RawEvidence,
		reportRawEvidenceFailure: options.ReportRawEvidenceFailure,
		ownerContext:             ownerContext,
		cancelOwner:              cancelOwner,
		operations:               make(map[*operation]struct{}),
		changed:                  make(chan struct{}),
	}, nil
}

func (pipeline *Pipeline) Execute(
	ctx context.Context,
	request ClientRequest,
	downstream Downstream,
) (result Result, resultErr error) {
	captured := &contentCapture{}
	result = Result{
		ExchangeID: request.exchangeID,
		Outcome:    AttemptFailed,
	}
	defer func() {
		pipeline.observeAttempt(request, result, resultErr, captured)
	}()
	defer func() {
		pipeline.observeContent(request, captured)
	}()
	if ctx == nil {
		return result, newFailure(
			ReasonInvalidExchangeRequest,
			request.exchangeID,
			0,
			errors.New("Exchange context is nil"),
		)
	}
	if err := request.validate(); err != nil {
		return result, newFailure(
			ReasonInvalidExchangeRequest,
			request.exchangeID,
			0,
			err,
		)
	}
	if downstream == nil {
		return result, newFailure(
			ReasonInvalidExchangeRequest,
			request.exchangeID,
			0,
			errors.New("downstream boundary is nil"),
		)
	}
	selection, err := selectFrozenPlan(request.plan)
	if err != nil {
		return result, newFailure(
			ReasonEnvironmentPlanInvalid,
			request.exchangeID,
			0,
			err,
		)
	}
	result.EnvironmentID = selection.environmentID.String()
	result.EnvironmentRevision = selection.environmentRevision
	result.EnvironmentDigest = selection.environmentDigest.String()
	result.EndpointID = selection.endpointID.String()
	result.EndpointRevision = selection.endpointRevision
	result.ProtocolPlanID = selection.protocolPlanID.String()
	result.ProtocolPlanRevision = selection.protocolPlanRevision
	result.RouteID = selection.routeID.String()
	result.RouteRevision = selection.routeRevision
	result.RouteHost = selection.target.NetworkHost()
	if err := validateClientOperation(request.plan, request.operation, request.replayClass); err != nil {
		return result, newFailure(
			ReasonEnvironmentPlanInvalid,
			request.exchangeID,
			0,
			err,
		)
	}

	operationContext, active, action, err := pipeline.begin(
		ctx,
		request.exchangeID,
	)
	if err != nil {
		result.Outcome = AttemptCanceled
		reason := ReasonExchangeCanceled
		switch {
		case errors.Is(err, ErrRuntimeStopping):
			reason = ReasonExchangeRuntimeStopping
		case errors.Is(err, errOfflineHoldAdmission):
			reason = ReasonOfflineHoldUnavailable
		}
		return result, newFailure(
			reason,
			request.exchangeID,
			0,
			err,
		)
	}
	defer pipeline.finish(active)
	defer action.Release()
	pipeline.observeStart(request)
	candidates, err := selection.credentialCandidates()
	if err != nil {
		return result, newFailure(
			ReasonEnvironmentPlanInvalid,
			request.exchangeID,
			0,
			err,
		)
	}

	// A request may be attempted against more than one account, but only
	// while the policy allows it and nothing has reached the client. The
	// ledger lives outside the loop because commits accumulate across the
	// whole logical Exchange, while everything a candidate decides does not.
	ledger := &CommitLedger{}
	transformTurn, err := newMessageTransformTurn(
		request,
		pipeline.annotations,
		pipeline.now().UTC(),
	)
	if err != nil {
		return result, newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			0,
			err,
		)
	}
	// attemptErr is the outcome the client ends up with. The loop body has its
	// own short-lived errors; this is the one that leaves.
	var attemptErr error
	for candidateIndex, candidate := range candidates {
		result.AccountID = ""
		result.AccountRevision = 0
		result.CredentialEpoch = 0
		// The translation report describes the attempt whose answer the client
		// is receiving, so it starts empty for each one.
		result.Translation = protocolcore.TranslationReport{}
		material, acquireErr := pipeline.acquireCredential(
			operationContext,
			selection,
			candidate,
		)
		if acquireErr != nil {
			attemptErr = newFailure(
				ReasonProviderCredentialUnavailable,
				request.exchangeID,
				0,
				acquireErr,
			)
		} else {
			if candidate.mode == providerauth.CredentialManaged {
				result.AccountID = material.account.ID
				result.AccountRevision = material.account.Revision
				result.CredentialEpoch = material.account.CredentialEpoch
			}
			attemptErr = pipeline.executeCandidate(
				operationContext,
				request,
				selection,
				material,
				action,
				downstream,
				ledger,
				&result,
				captured,
				transformTurn,
			)
			material.Release()
		}

		if attemptErr == nil {
			break
		}
		if !mayTryNextCandidate(
			selection.accountPolicy.FailoverPolicy(),
			len(candidates),
			candidateIndex,
			ledger,
			request.replayClass,
			attemptErr,
		) {
			break
		}
	}
	err = attemptErr
	result.Ledger = ledger.Snapshot()
	if err != nil {
		switch {
		case ReasonOf(err) == ReasonToolDecisionRejected ||
			ReasonOf(err) == ReasonToolDecisionExpired ||
			ReasonOf(err) == ReasonToolDecisionUnavailable:
			result.Outcome = AttemptAborted
		case errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(context.Cause(operationContext), ErrRuntimeStopping):
			result.Outcome = AttemptCanceled
		}
		return result, err
	}
	result.Outcome = AttemptSucceeded
	return result, nil
}

func (pipeline *Pipeline) executeCandidate(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	credential credentialMaterial,
	action *offlinehold.ActionLease,
	downstream Downstream,
	ledger *CommitLedger,
	result *Result,
	captured *contentCapture,
	transformTurn *messagetransform.PipelineTurn,
) error {
	if selection.original {
		if credential.mode != providerauth.CredentialClientPassthrough {
			return newFailure(
				ReasonEnvironmentPlanInvalid,
				request.exchangeID,
				0,
				errors.New("original passthrough selected a managed credential"),
			)
		}
		headers, available := request.OriginalHeaders()
		if !available {
			return newFailure(
				ReasonEnvironmentPlanInvalid,
				request.exchangeID,
				0,
				errors.New("original passthrough request envelope is unavailable"),
			)
		}
		transformedHeaders, transformedBody, transformInput, err := applyRequestMessageTransform(
			ctx,
			transformTurn,
			request.operation.Method(),
			request.operation.Path(),
			headers,
			request.body,
		)
		if err != nil {
			return newFailure(ReasonMessageTransformFailed, request.exchangeID, 0, err)
		}
		frozenRequest, err := pipeline.newProviderRequest(
			request,
			selection,
			credential,
			action,
			request.operation.Method(),
			strings.TrimPrefix(request.operation.Path(), "/"),
			request.operation.RawQuery(),
			transformedHeaders,
			transformedBody,
		)
		if err != nil {
			return newFailure(ReasonProviderRequestInvalid, request.exchangeID, 0, err)
		}
		pipeline.observeMessageTransformRequest(ctx, frozenRequest, transformInput)
		var contentPath *protocolpath.Path
		var decodedContent *protocolcore.Request
		contentBody, contentErr := decodeBoundedContent(
			request.body,
			headers.Get("Content-Encoding"),
		)
		if candidatePath, selectErr := pipeline.protocolPaths.Select(
			selection.codecPlan,
			request.operation.id,
		); selectErr == nil && contentErr == nil {
			if decoded, _, decodeErr := candidatePath.Client().DecodeRequest(contentBody); decodeErr == nil {
				decoded = mergeClientProtocolEvidence(
					decoded,
					request.ClientProtocolEvidence(),
				)
				contentPath = candidatePath
				decodedContent = &decoded
				decodedForEvidence := decoded.Clone()
				captured.request = &decodedForEvidence
				if request.plan.ContentRecording().Mode != environment.ContentRecordingOff {
					pipeline.observeRequest(request, captured)
				}
			}
		}
		return pipeline.executeOriginal(
			ctx,
			request,
			frozenRequest,
			downstream,
			ledger,
			result,
			contentPath,
			decodedContent,
			captured,
			transformTurn,
		)
	}

	protocolPath, err := pipeline.protocolPaths.Select(
		selection.codecPlan,
		request.operation.id,
	)
	if err != nil {
		return newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
	}
	decoded, clientRequestReport, err := protocolPath.Client().DecodeRequest(request.body)
	result.Translation = result.Translation.Merge(clientRequestReport)
	if err != nil {
		reason := ReasonInvalidExchangeRequest
		if protocolcore.ReasonOf(err) == protocolcore.ReasonUnsupportedClientInput {
			reason = ReasonUnsupportedClientInput
		}
		failure := newFailure(reason, request.exchangeID, 0, err)
		failure.ClientField = classifyClientRequestField(request.body, err)
		return failure
	}
	if mappedModel, mapped := selection.mappedModel(decoded.RequestedModel); mapped {
		decoded, err = decoded.WithEffectiveModel(mappedModel)
		if err != nil {
			return newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
		}
	}
	decoded = mergeClientProtocolEvidence(
		decoded,
		request.ClientProtocolEvidence(),
	)
	decodedForEvidence := decoded.Clone()
	captured.request = &decodedForEvidence
	pipeline.observeRequest(request, captured)
	encodedProvider, backendRequestReport, err := protocolPath.EncodeProviderRequest(
		decoded,
		request.body,
		request.protocolHeaders(),
	)
	result.Translation = result.Translation.Merge(backendRequestReport)
	if err != nil {
		return newFailure(ReasonProviderRequestInvalid, request.exchangeID, 0, err)
	}
	headers := encodedProvider.Headers()
	if credential.mode == providerauth.CredentialClientPassthrough {
		original, available := request.OriginalHeaders()
		if !available {
			return newFailure(
				ReasonEnvironmentPlanInvalid,
				request.exchangeID,
				0,
				errors.New("client passthrough credentials are unavailable"),
			)
		}
		for name, values := range headers {
			original[name] = slices.Clone(values)
		}
		headers = original
	}
	transformedHeaders, transformedBody, transformInput, err := applyRequestMessageTransform(
		ctx,
		transformTurn,
		encodedProvider.Method(),
		"/"+strings.TrimPrefix(encodedProvider.RelativePath(), "/"),
		headers,
		encodedProvider.Body(),
	)
	if err != nil {
		return newFailure(ReasonMessageTransformFailed, request.exchangeID, 0, err)
	}
	frozenRequest, err := pipeline.newProviderRequest(
		request,
		selection,
		credential,
		action,
		encodedProvider.Method(),
		encodedProvider.RelativePath(),
		"",
		transformedHeaders,
		transformedBody,
	)
	if err != nil {
		return newFailure(ReasonProviderRequestInvalid, request.exchangeID, 0, err)
	}
	pipeline.observeMessageTransformRequest(ctx, frozenRequest, transformInput)
	if decoded.Stream {
		return pipeline.executeStream(
			ctx,
			request,
			selection,
			protocolPath,
			decoded,
			frozenRequest,
			downstream,
			ledger,
			result,
			captured,
			transformTurn,
		)
	}
	return pipeline.executeComplete(
		ctx,
		request,
		selection,
		protocolPath,
		decoded,
		frozenRequest,
		downstream,
		ledger,
		result,
		captured,
		transformTurn,
	)
}

// decodeBoundedContent exposes one bounded logical HTTP representation to
// protocol evidence and message transforms. Callers decide whether the exact
// compressed bytes continue over their transport boundary.
func decodeBoundedContent(body []byte, contentEncoding string) ([]byte, error) {
	switch encoding := strings.ToLower(strings.TrimSpace(contentEncoding)); encoding {
	case "", "identity":
		return bytes.Clone(body), nil
	case "gzip":
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		decoded, readErr := readBounded(reader, maxCompleteResponseBytes)
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		return decoded, nil
	case "zstd":
		reader, err := zstd.NewReader(
			bytes.NewReader(body),
			zstd.WithDecoderMaxMemory(maxCompleteResponseBytes),
		)
		if err != nil {
			return nil, err
		}
		decoded, readErr := readBounded(reader, maxCompleteResponseBytes)
		reader.Close()
		return decoded, readErr
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding %q", encoding)
	}
}

func (pipeline *Pipeline) newProviderRequest(
	request ClientRequest,
	selection frozenSelection,
	credential credentialMaterial,
	action *offlinehold.ActionLease,
	method string,
	relativePath string,
	rawQuery string,
	headers http.Header,
	body []byte,
) (providertransport.Request, error) {
	attemptID, err := pipeline.attemptIDs.NewAttemptID()
	if err != nil {
		return providertransport.Request{}, err
	}
	egressID, err := pipeline.attemptIDs.NewAttemptID()
	if err != nil {
		return providertransport.Request{}, err
	}
	clientHello, _ := request.ClientHelloObservation()
	rawContext, rawContextErr := newProviderRawEvidenceContext(
		request,
		selection,
		credential,
		egressID,
	)
	var rawEvidence *rawevidence.Context
	if rawContextErr == nil {
		rawEvidence = &rawContext
	}
	options := providertransport.RequestOptions{
		RequestID: attemptID, EgressAttemptID: egressID,
		ConnectionID: request.connectionRef, ExchangeID: request.exchangeID,
		ParentAttemptID: attemptID, TargetRef: selection.targetRef,
		Target: selection.target, Provenance: selection.provenance, Action: action,
		Method: method, RelativePath: relativePath, RawQuery: rawQuery,
		Headers: headers, Body: body, CredentialMode: credential.mode,
		WireProfile: selection.wireProfile, ClientProtocol: request.ClientHTTPProtocol(),
		ClientUserAgent: request.ClientUserAgent(), ClientHello: clientHello,
		EgressPolicy: request.RequestPlan().EgressPolicy(),
		RawEvidence:  rawEvidence,
	}
	if credential.mode == providerauth.CredentialClientPassthrough {
		options.PassthroughOrigin = selection.target.Origin()
	} else {
		options.AccountRef = credential.account
		options.SecretRef = credential.secret
		options.AuthDriverRef = credential.driver
	}
	return providertransport.NewRequest(options)
}

func newProviderRawEvidenceContext(
	request ClientRequest,
	selection frozenSelection,
	credential credentialMaterial,
	attemptID string,
) (rawevidence.Context, error) {
	context, err := request.RawEvidenceContext()
	if err != nil {
		return rawevidence.Context{}, err
	}
	if context.RouteID != selection.routeID.String() ||
		context.UpstreamEndpointID != selection.targetRef {
		return rawevidence.Context{}, errors.New(
			"provider raw evidence selection does not match the frozen request",
		)
	}
	context.AttemptID = attemptID
	if credential.mode == providerauth.CredentialManaged {
		context.AccountID = credential.account.ID
		context.AccountRevision = credential.account.Revision
		context.CredentialEpoch = credential.account.CredentialEpoch
	}
	if err := context.Validate(); err != nil {
		return rawevidence.Context{}, fmt.Errorf(
			"freeze provider raw evidence context: %w",
			err,
		)
	}
	return context, nil
}

func (pipeline *Pipeline) observeMessageTransformRequest(
	ctx context.Context,
	request providertransport.Request,
	input messagetransform.RequestMessage,
) {
	if input.Method == "" {
		return
	}
	pipeline.observeMessageTransform(ctx, request, rawevidence.Observation{
		Layer:          rawevidence.LayerTransformRequestInput,
		Method:         input.Method,
		Path:           input.Path,
		Headers:        input.Headers.Clone(),
		Body:           bytes.Clone(input.Body),
		Complete:       true,
		Representation: "message_transform_input",
		ContentType:    input.Headers.Get("Content-Type"),
	})
}

func (pipeline *Pipeline) observeMessageTransformResponse(
	ctx context.Context,
	request providertransport.Request,
	input messagetransform.ResponseMessage,
) {
	if input.StatusCode == 0 {
		return
	}
	pipeline.observeMessageTransform(ctx, request, rawevidence.Observation{
		Layer:          rawevidence.LayerTransformResponseInput,
		StatusCode:     input.StatusCode,
		Headers:        input.Headers.Clone(),
		Body:           bytes.Clone(input.Body),
		Complete:       true,
		Representation: "message_transform_input",
		ContentType:    input.Headers.Get("Content-Type"),
	})
}

func (pipeline *Pipeline) observeMessageTransformStreamResponse(
	ctx context.Context,
	request providertransport.Request,
	transformer *streamMessageTransformer,
) {
	input, total, complete := transformer.Input()
	if input.StatusCode == 0 {
		return
	}
	reason := ""
	if !complete {
		reason = "transform_input_limit"
	}
	pipeline.observeMessageTransform(ctx, request, rawevidence.Observation{
		Layer:            rawevidence.LayerTransformResponseInput,
		StatusCode:       input.StatusCode,
		Headers:          input.Headers.Clone(),
		Body:             bytes.Clone(input.Body),
		TotalBodyBytes:   total,
		Complete:         complete,
		IncompleteReason: reason,
		Representation:   "message_transform_stream_input",
		ContentType:      input.Headers.Get("Content-Type"),
	})
}

func (pipeline *Pipeline) observeMessageTransform(
	ctx context.Context,
	request providertransport.Request,
	observation rawevidence.Observation,
) {
	if pipeline == nil || pipeline.rawEvidence == nil {
		return
	}
	rawContext, ok := request.RawEvidenceContext()
	if !ok || rawContext.Recording == rawevidence.RecordingOff {
		return
	}
	observation.Context = rawContext
	operation, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		pipeline.observeLimit,
	)
	defer cancel()
	if _, err := pipeline.rawEvidence.Observe(operation, observation); err != nil &&
		pipeline.reportRawEvidenceFailure != nil {
		pipeline.reportRawEvidenceFailure(fmt.Errorf(
			"record message Transform input evidence: %w", err,
		))
	}
}

// RawEvidenceContext freezes the Exchange-level authority shared by the
// client ingress and downstream envelopes. It deliberately omits Attempt and
// Account: those belong to an actual provider attempt and are attached by the
// provider transport. A retry can therefore never rewrite the client-facing
// envelope as though it belonged to only one candidate account.
func (request ClientRequest) RawEvidenceContext() (rawevidence.Context, error) {
	selection, err := selectFrozenPlan(request.RequestPlan())
	if err != nil {
		return rawevidence.Context{}, fmt.Errorf(
			"freeze client raw evidence selection: %w",
			err,
		)
	}
	recordingPolicy := request.RequestPlan().ContentRecording()
	recording := rawevidence.RecordingMode(recordingPolicy.Mode)
	if !recording.Valid() {
		return rawevidence.Context{}, errors.New(
			"client raw evidence recording mode is invalid",
		)
	}
	scopeKind := rawevidence.ScopeRuntime
	scopeID := ""
	if captureRunID := request.CaptureRunRef(); captureRunID != "" {
		scopeKind = rawevidence.ScopeManagedRun
		scopeID = captureRunID
	} else if manualCaptureID := request.ManualCaptureRef(); manualCaptureID != "" {
		scopeKind = rawevidence.ScopeManualCapture
		scopeID = manualCaptureID
	}
	context := rawevidence.Context{
		ScopeKind:                scopeKind,
		ScopeID:                  scopeID,
		ExchangeID:               request.ExchangeID(),
		ConnectionID:             request.ConnectionRef(),
		EnvironmentID:            selection.environmentID.String(),
		EnvironmentRevision:      uint64(selection.environmentRevision),
		EnvironmentDigest:        selection.environmentDigest.String(),
		ClientEndpointID:         selection.endpointID.String(),
		ClientEndpointRevision:   uint64(selection.endpointRevision),
		UpstreamEndpointID:       selection.targetRef,
		UpstreamEndpointRevision: uint64(selection.targetRevision),
		ProtocolPlanID:           selection.protocolPlanID.String(),
		ProtocolPlanRevision:     uint64(selection.protocolPlanRevision),
		RouteID:                  selection.routeID.String(),
		RouteRevision:            uint64(selection.routeRevision),
		Recording:                recording,
		RetentionDays:            recordingPolicy.RetentionDays,
	}
	if err := context.Validate(); err != nil {
		return rawevidence.Context{}, fmt.Errorf(
			"freeze client raw evidence context: %w",
			err,
		)
	}
	return context, nil
}

func (pipeline *Pipeline) acquireCredential(
	ctx context.Context,
	selection frozenSelection,
	candidate credentialCandidate,
) (credentialMaterial, error) {
	if candidate.mode == providerauth.CredentialClientPassthrough {
		return credentialMaterial{mode: providerauth.CredentialClientPassthrough}, nil
	}
	if candidate.mode != providerauth.CredentialManaged || pipeline.accounts == nil {
		return credentialMaterial{}, errors.New("managed account authority is unavailable")
	}
	request := AccountLeaseRequest{
		environmentID:            selection.environmentID,
		environmentRevision:      selection.environmentRevision,
		environmentDigest:        selection.environmentDigest,
		routeID:                  selection.routeID,
		routeRevision:            selection.routeRevision,
		upstreamEndpointID:       candidate.account.UpstreamEndpointID,
		upstreamEndpointRevision: candidate.account.UpstreamEndpointRevision,
		accountID:                candidate.account.ID,
		accountRevision:          candidate.account.Revision,
		realmID:                  candidate.account.RealmID,
	}
	lease, err := pipeline.accounts.Acquire(ctx, request)
	if err != nil {
		return credentialMaterial{}, err
	}
	if lease == nil {
		return credentialMaterial{}, errors.New("managed account authority returned no lease")
	}
	fail := func(err error) (credentialMaterial, error) {
		lease.Release()
		return credentialMaterial{}, err
	}
	account, available := lease.Account()
	if lease.Mode() != providerauth.CredentialManaged || !available ||
		account.Validate() != nil || account.ID != candidate.account.ID ||
		account.Revision != uint64(candidate.account.Revision) ||
		account.RealmID != candidate.account.RealmID {
		return fail(errors.New("managed account lease does not match the frozen route"))
	}
	if lease.Driver().String() == "" || lease.Secret().String() == "" {
		return fail(errors.New("managed account lease is incomplete"))
	}
	return credentialMaterial{
		mode: providerauth.CredentialManaged, account: account,
		secret: lease.Secret(), driver: lease.Driver(), release: lease.Release,
	}, nil
}

func (pipeline *Pipeline) observeAttempt(
	request ClientRequest,
	result Result,
	resultErr error,
	captured *contentCapture,
) {
	if pipeline == nil || pipeline.observer == nil {
		return
	}
	admission, hasAdmission := request.CaptureAdmission()
	plan := request.plan
	endpoint := plan.Endpoint()
	protocolPlan := plan.ProtocolPlan()
	routeID, routeRevision := requestPlanRouteRef(plan)
	conversation := terminalConversationRef(request, captured)
	observation := AttemptObservation{
		ExchangeID:          request.exchangeID,
		EnvironmentID:       plan.EnvironmentID(),
		EnvironmentRevision: plan.EnvironmentRevision(),
		EnvironmentDigest:   plan.EnvironmentDigest().String(),
		EndpointID:          endpoint.ID(), EndpointRevision: endpoint.Revision(),
		ProtocolPlanID: protocolPlan.ID(), ProtocolPlanRevision: protocolPlan.Revision(),
		RouteID: routeID, RouteRevision: routeRevision,
		AccountID: result.AccountID, AccountRevision: result.AccountRevision,
		CredentialEpoch: result.CredentialEpoch, Admission: admission,
		HasAdmission: hasAdmission, ConnectionID: request.connectionRef,
		Outcome: result.Outcome, ReasonCode: ReasonOf(resultErr),
		Presentation: result.Presentation, Transport: result.Transport.Clone(),
		Conversation:           conversation,
		ClientProtocolEvidence: request.ClientProtocolEvidence(),
	}
	if captured != nil && captured.response != nil {
		observation.ProviderResponseID = captured.response.ID
	}
	var failure *Failure
	if errors.As(resultErr, &failure) {
		observation.ProviderStatus = failure.ProviderStatus
		observation.ProviderField = failure.ProviderField
		observation.ClientField = failure.ClientField
		observation.ClientPath = failure.ClientPath
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(pipeline.ownerContext),
		pipeline.observeLimit,
	)
	defer cancel()
	_ = pipeline.observer.ObserveTerminal(ctx, observation)
}

func (pipeline *Pipeline) observeStart(request ClientRequest) {
	if pipeline == nil || pipeline.observer == nil {
		return
	}
	admission, hasAdmission := request.CaptureAdmission()
	plan := request.plan
	endpoint := plan.Endpoint()
	protocolPlan := plan.ProtocolPlan()
	routeID, routeRevision := requestPlanRouteRef(plan)
	conversation := agentconversation.Ref{}
	evidence := request.ClientProtocolEvidence()
	semanticRequest := protocolcore.Request{ProtocolEvidence: evidence}
	body := request.body
	if headers, available := request.OriginalHeaders(); available {
		if decodedBody, decodeErr := decodeBoundedContent(
			request.body,
			headers.Get("Content-Encoding"),
		); decodeErr == nil {
			body = decodedBody
		}
	}
	if protocolPath, selectErr := pipeline.protocolPaths.Select(
		plan.CodecPlan(),
		request.operation.id,
	); selectErr == nil {
		if decoded, _, decodeErr := protocolPath.Client().DecodeRequest(body); decodeErr == nil {
			semanticRequest = mergeClientProtocolEvidence(decoded, evidence)
			evidence = append(
				[]protocolcore.ProtocolEvidenceValue(nil),
				semanticRequest.ProtocolEvidence...,
			)
		}
	}
	if len(evidence) != 0 {
		captureRunID := ""
		sourceDisplayName := "ViberMate runtime"
		if hasAdmission {
			captureRunID, _ = admission.CaptureRunID()
			sourceDisplayName = admission.SourceLabel()
		}
		candidate, projectErr := agentconversation.Project(agentconversation.ProjectionInput{
			CaptureRunID:      captureRunID,
			ExchangeID:        request.exchangeID,
			SourceDisplayName: sourceDisplayName,
			Request:           &semanticRequest,
		})
		if projectErr == nil &&
			(candidate.Evidence == agentconversation.EvidenceExplicitSession ||
				candidate.Evidence == agentconversation.EvidenceExplicitActor) {
			conversation = candidate
		}
	}
	if conversation.ProjectionID == "" {
		var err error
		conversation, err = agentconversation.Pending(request.exchangeID)
		if err != nil {
			return
		}
	}
	observation := StartObservation{
		ExchangeID:          request.exchangeID,
		EnvironmentID:       plan.EnvironmentID(),
		EnvironmentRevision: plan.EnvironmentRevision(),
		EnvironmentDigest:   plan.EnvironmentDigest().String(),
		EndpointID:          endpoint.ID(), EndpointRevision: endpoint.Revision(),
		ProtocolPlanID: protocolPlan.ID(), ProtocolPlanRevision: protocolPlan.Revision(),
		RouteID: routeID, RouteRevision: routeRevision,
		Admission: admission, HasAdmission: hasAdmission,
		ConnectionID:           request.connectionRef,
		Conversation:           conversation,
		ClientProtocolEvidence: evidence,
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(pipeline.ownerContext),
		pipeline.observeLimit,
	)
	defer cancel()
	_ = pipeline.observer.ObserveStart(ctx, observation)
}

// mergeClientProtocolEvidence attaches exact, non-secret ingress identifiers
// to the decoded semantic request. A protocol decoder remains authoritative
// when it already supplied the same names; the ingress allowlist only fills
// absent values and can therefore never rewrite decoded semantics.
func mergeClientProtocolEvidence(
	request protocolcore.Request,
	incoming []protocolcore.ProtocolEvidenceValue,
) protocolcore.Request {
	if len(incoming) == 0 || protocolcore.ValidateProtocolEvidence(incoming) != nil {
		return request
	}
	merged := request.Clone()
	present := make(map[string]struct{}, len(merged.ProtocolEvidence))
	for _, value := range merged.ProtocolEvidence {
		present[value.Name] = struct{}{}
	}
	for _, value := range incoming {
		if _, exists := present[value.Name]; exists {
			continue
		}
		merged.ProtocolEvidence = append(merged.ProtocolEvidence, value)
		present[value.Name] = struct{}{}
	}
	slices.SortFunc(
		merged.ProtocolEvidence,
		func(left, right protocolcore.ProtocolEvidenceValue) int {
			return strings.Compare(left.Name, right.Name)
		},
	)
	if protocolcore.ValidateProtocolEvidence(merged.ProtocolEvidence) != nil {
		return request
	}
	return merged
}

func terminalConversationRef(
	request ClientRequest,
	captured *contentCapture,
) agentconversation.Ref {
	admission, admitted := request.CaptureAdmission()
	captureRunID := ""
	sourceDisplayName := "ViberMate runtime"
	if admitted {
		captureRunID, _ = admission.CaptureRunID()
		sourceDisplayName = admission.SourceLabel()
	}
	var decodedRequest *protocolcore.Request
	var decodedResponse *protocolcore.Response
	if captured != nil {
		decodedRequest = captured.request
		decodedResponse = captured.response
	}
	ref, err := agentconversation.Project(agentconversation.ProjectionInput{
		CaptureRunID:      captureRunID,
		ExchangeID:        request.exchangeID,
		SourceDisplayName: sourceDisplayName,
		Request:           decodedRequest,
		Response:          decodedResponse,
	})
	if err == nil {
		return ref
	}
	// A structural projection failure must never erase the terminal Activity.
	// Fall back to the narrowest provable boundary, which is this Exchange.
	ref, _ = agentconversation.Project(agentconversation.ProjectionInput{
		ExchangeID: request.exchangeID,
	})
	return ref
}

func (pipeline *Pipeline) observeRequest(
	request ClientRequest,
	captured *contentCapture,
) {
	if captured == nil || captured.requestObserved || captured.request == nil {
		return
	}
	captured.requestObserved = true
	pipeline.observeContent(request, captured)
}

func (pipeline *Pipeline) observeContent(
	request ClientRequest,
	captured *contentCapture,
) {
	if pipeline == nil || pipeline.content == nil || captured == nil ||
		captured.request == nil ||
		request.plan.ContentRecording().Mode == environment.ContentRecordingOff {
		return
	}
	plan := request.plan
	endpoint := plan.Endpoint()
	protocolPlan := plan.ProtocolPlan()
	routeID, routeRevision := requestPlanRouteRef(plan)
	observation := ContentObservation{
		ExchangeID:      request.exchangeID,
		CaptureRunID:    request.CaptureRunRef(),
		ManualCaptureID: request.ManualCaptureRef(),
		EnvironmentID:   plan.EnvironmentID(), EnvironmentRevision: plan.EnvironmentRevision(),
		EnvironmentDigest: plan.EnvironmentDigest().String(),
		EndpointID:        endpoint.ID(), EndpointRevision: endpoint.Revision(),
		ProtocolPlanID: protocolPlan.ID(), ProtocolPlanRevision: protocolPlan.Revision(),
		RouteID: routeID, RouteRevision: routeRevision,
		Recording: plan.ContentRecording(), Request: captured.request.Clone(),
	}
	if captured.response != nil {
		response := captured.response.Clone()
		observation.Response = &response
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(pipeline.ownerContext),
		pipeline.observeLimit,
	)
	defer cancel()
	_ = pipeline.content.ObserveContent(ctx, observation)
}

func requestPlanRouteRef(
	plan environment.RequestPlan,
) (environment.UpstreamRouteID, environment.Revision) {
	route, exists := plan.UpstreamRoute()
	if !exists {
		return "", 0
	}
	return route.ID(), route.Revision()
}

func classifyClientRequestField(body []byte, failure error) ClientField {
	pathField := ClientFieldUnknown
	var protocolFailure *protocolcore.Failure
	if errors.As(failure, &protocolFailure) {
		pathField = clientFieldFromPath(protocolFailure.Path)
		switch pathField {
		case ClientFieldUnknown,
			ClientFieldOutputConfig,
			ClientFieldOutputConfigEffort:
		default:
			return pathField
		}
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return ClientFieldUnknown
	}
	supported := map[string]struct{}{
		"model": {}, "max_tokens": {}, "messages": {}, "system": {},
		"metadata": {}, "stop_sequences": {}, "stream": {},
		"temperature": {}, "top_p": {}, "top_k": {}, "tools": {},
		"tool_choice": {}, "thinking": {}, "output_config": {},
		"context_management": {}, "diagnostics": {}, "service_tier": {},
	}
	for _, field := range []ClientField{
		ClientFieldCacheControl,
		ClientFieldOutputConfig,
		ClientFieldContainer,
		ClientFieldInferenceGeo,
		ClientFieldContextManagement,
		ClientFieldMCPServers,
		ClientFieldSpeed,
		ClientFieldFallbacks,
		ClientFieldDiagnostics,
		ClientFieldOutputFormat,
	} {
		if _, present := root[string(field)]; present {
			if _, accepted := supported[string(field)]; !accepted {
				return field
			}
		}
	}
	if raw, present := root[string(ClientFieldOutputConfig)]; present {
		if field := classifyOutputConfigField(raw); field != ClientFieldUnknown {
			return field
		}
	}
	if pathField == ClientFieldOutputConfig ||
		pathField == ClientFieldOutputConfigEffort {
		return pathField
	}
	return ClientFieldUnknown
}

func classifyOutputConfigField(raw json.RawMessage) ClientField {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return ClientFieldOutputConfig
	}
	for name := range object {
		switch name {
		case "effort", "format", "task_budget":
		default:
			return ClientFieldOutputConfigUnknown
		}
	}
	if value, present := object["format"]; present &&
		string(bytes.TrimSpace(value)) != "null" {
		return ClientFieldOutputConfigFormat
	}
	if rawBudget, present := object["task_budget"]; present &&
		!validTaskBudgetShape(rawBudget) {
		return ClientFieldOutputConfigTaskBudget
	}
	if rawEffort, present := object["effort"]; present {
		var effort string
		if json.Unmarshal(rawEffort, &effort) != nil {
			return ClientFieldOutputConfigEffortInvalid
		}
		switch effort {
		case "none":
			return ClientFieldOutputConfigEffortNone
		case "minimal":
			return ClientFieldOutputConfigEffortMinimal
		case "low", "medium", "high", "xhigh", "max":
			return ClientFieldUnknown
		default:
			return ClientFieldOutputConfigEffortInvalid
		}
	}
	return ClientFieldUnknown
}

func validTaskBudgetShape(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	for name := range object {
		switch name {
		case "type", "total", "remaining":
		default:
			return false
		}
	}
	var budget struct {
		Type      string `json:"type"`
		Total     int    `json:"total"`
		Remaining *int   `json:"remaining"`
	}
	if json.Unmarshal(raw, &budget) != nil ||
		budget.Type != "tokens" ||
		budget.Total <= 0 {
		return false
	}
	return budget.Remaining == nil ||
		(*budget.Remaining >= 0 && *budget.Remaining <= budget.Total)
}

func clientFieldFromPath(path string) ClientField {
	for _, field := range []struct {
		path  string
		field ClientField
	}{
		{"$.output_config.task_budget", ClientFieldOutputConfigTaskBudget},
		{"$.output_config.format", ClientFieldOutputConfigFormat},
		{"$.output_config.effort", ClientFieldOutputConfigEffort},
	} {
		if path == field.path ||
			strings.HasPrefix(path, field.path+".") ||
			strings.HasPrefix(path, field.path+"[") {
			return field.field
		}
	}
	for _, field := range []ClientField{
		ClientFieldModel,
		ClientFieldMaxTokens,
		ClientFieldMessages,
		ClientFieldSystem,
		ClientFieldMetadata,
		ClientFieldStopSequences,
		ClientFieldStream,
		ClientFieldTemperature,
		ClientFieldTopP,
		ClientFieldTopK,
		ClientFieldTools,
		ClientFieldToolChoice,
		ClientFieldThinking,
		ClientFieldServiceTier,
		ClientFieldCacheControl,
		ClientFieldOutputConfig,
		ClientFieldContainer,
		ClientFieldInferenceGeo,
		ClientFieldContextManagement,
		ClientFieldMCPServers,
		ClientFieldSpeed,
		ClientFieldFallbacks,
		ClientFieldDiagnostics,
		ClientFieldOutputFormat,
	} {
		prefix := "$." + string(field)
		if path == prefix ||
			strings.HasPrefix(path, prefix+".") ||
			strings.HasPrefix(path, prefix+"[") {
			return field
		}
	}
	return ClientFieldUnknown
}

func (pipeline *Pipeline) executeComplete(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	protocolPath *protocolpath.Path,
	decoded protocolcore.Request,
	frozenRequest providertransport.Request,
	downstream Downstream,
	ledger *CommitLedger,
	result *Result,
	captured *contentCapture,
	transformTurn *messagetransform.PipelineTurn,
) error {
	result.Presentation = frozenRequest.WirePresentationEvidence()
	if err := ledger.RecordUpstreamSend(int64(len(frozenRequest.Body()))); err != nil {
		return newFailure(
			ReasonProviderRequestInvalid,
			request.exchangeID,
			0,
			err,
		)
	}
	response, evidence, err := pipeline.provider.Do(ctx, frozenRequest)
	result.Credential = evidence.Credential
	if evidence.Presentation.RequestedRef != "" {
		result.Presentation = evidence.Presentation
	}
	result.Transport = evidence.Transport
	if err != nil {
		return pipeline.classifyProviderError(ctx, request.exchangeID, err)
	}
	if response == nil || response.Body == nil {
		return newFailure(
			ReasonProviderTransportFailed,
			request.exchangeID,
			0,
			errors.New("provider returned an incomplete response"),
		)
	}
	defer response.Body.Close()
	ledger.RecordUpstreamResponse()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return newProviderStatusFailure(
			request.exchangeID,
			response.StatusCode,
			classifyProviderRejection(response.Body),
		)
	}
	if !contentTypeMatches(response.Header.Get("Content-Type"), "application/json") {
		return newProviderContentTypeFailure(
			request.exchangeID,
			response.StatusCode,
			response.Body,
			"application/json",
		)
	}
	body, err := readBounded(response.Body, maxCompleteResponseBytes)
	if err != nil {
		return newFailure(
			ReasonProviderResponseInvalid,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	responseEnvelope := managedResponseEnvelope(ResponseModeJSON)
	if transformTurn.HasResponse() {
		transformed, transformInput, _, transformErr := applyResponseMessageTransform(
			ctx,
			transformTurn,
			response.StatusCode,
			response.Header,
			body,
		)
		if transformErr != nil {
			return newFailure(
				ReasonMessageTransformFailed,
				request.exchangeID,
				response.StatusCode,
				transformErr,
			)
		}
		pipeline.observeMessageTransformResponse(ctx, frozenRequest, transformInput)
		responseEnvelope, transformErr = managedEnvelopeWithTransform(
			ResponseModeJSON,
			transformInput.Headers,
			transformed.Headers,
		)
		if transformErr != nil {
			return newFailure(
				ReasonMessageTransformFailed,
				request.exchangeID,
				response.StatusCode,
				transformErr,
			)
		}
		body = transformed.Body
	}
	providerResponse, backendResponseReport, err :=
		protocolPath.Backend().DecodeResponse(
			decoded,
			body,
		)
	result.Translation = result.Translation.Merge(backendResponseReport)
	if err != nil {
		return newFailure(
			ReasonProviderResponseInvalid,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	intents := responseToolIntents(providerResponse)
	if err := pipeline.decideTools(ctx, request, selection, decoded, intents); err != nil {
		result.Outcome = AttemptAborted
		return err
	}
	clientBody, clientResponseReport, err :=
		protocolPath.EncodeClientResponse(
			decoded,
			providerResponse,
			body,
		)
	result.Translation = result.Translation.Merge(clientResponseReport)
	if err != nil {
		return newFailure(
			ReasonProviderResponseInvalid,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if err := downstream.Begin(ctx, responseEnvelope); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			0,
			err,
		)
	}
	if err := ledger.RecordOrdinaryHeaders(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			0,
			err,
		)
	}
	committed, writeErr := writeDownstream(ctx, downstream, clientBody)
	if committed > 0 {
		if err := ledger.RecordSemanticWrite(committed); err != nil {
			return newFailure(
				ReasonDownstreamCommitFailed,
				request.exchangeID,
				0,
				err,
			)
		}
		if len(intents) > 0 {
			if err := ledger.RecordToolExposure(toolKeys(intents)); err != nil {
				return newFailure(
					ReasonDownstreamCommitFailed,
					request.exchangeID,
					0,
					err,
				)
			}
		}
	}
	if writeErr != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			0,
			writeErr,
		)
	}
	if err := ledger.RecordTerminal(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			0,
			err,
		)
	}
	responseForEvidence := providerResponse.Clone()
	captured.response = &responseForEvidence
	return nil
}

// executeOriginal preserves the bounded same-dialect request and response body
// bytes plus end-to-end headers after mandatory proxy sanitation. HTTP framing,
// hop-by-hop fields, TLS state, and connection reuse are transport-owned and are
// not claimed to pass through byte for byte. The exchange still crosses the
// provider transport, Offline Hold, and EgressAttempt boundaries; only codec,
// model, credential, plugin, retry, and tool-decision mutation are absent.
func (pipeline *Pipeline) executeOriginal(
	ctx context.Context,
	request ClientRequest,
	frozenRequest providertransport.Request,
	downstream Downstream,
	ledger *CommitLedger,
	result *Result,
	contentPath *protocolpath.Path,
	decodedContent *protocolcore.Request,
	captured *contentCapture,
	transformTurn *messagetransform.PipelineTurn,
) error {
	result.Presentation = frozenRequest.WirePresentationEvidence()
	if err := ledger.RecordUpstreamSend(int64(len(frozenRequest.Body()))); err != nil {
		return newFailure(
			ReasonProviderRequestInvalid,
			request.exchangeID,
			0,
			err,
		)
	}
	response, evidence, err := pipeline.provider.Do(ctx, frozenRequest)
	result.Credential = evidence.Credential
	if evidence.Presentation.RequestedRef != "" {
		result.Presentation = evidence.Presentation
	}
	result.Transport = evidence.Transport
	if err != nil {
		return pipeline.classifyProviderError(ctx, request.exchangeID, err)
	}
	if response == nil || response.Body == nil {
		return newFailure(
			ReasonProviderTransportFailed,
			request.exchangeID,
			0,
			errors.New("provider returned an incomplete response"),
		)
	}
	defer response.Body.Close()
	ledger.RecordUpstreamResponse()
	mode := ResponseModeJSON
	responseContentType := response.Header.Get("Content-Type")
	if contentTypeMatches(responseContentType, "text/event-stream") ||
		(responseContentType == "" && decodedContent != nil && decodedContent.Stream) {
		// The Codex ChatGPT transport currently omits Content-Type while returning
		// a Responses event stream. Only use the already-validated semantic
		// request as a fallback when the header is absent; an explicit response
		// media type remains authoritative.
		mode = ResponseModeEventStream
	}
	if transformTurn.HasResponse() {
		if mode == ResponseModeEventStream {
			return pipeline.executeTransformedOriginalStream(
				ctx,
				request,
				frozenRequest,
				response,
				downstream,
				ledger,
				contentPath,
				decodedContent,
				captured,
				transformTurn,
			)
		}
		return pipeline.executeTransformedOriginalComplete(
			ctx,
			request,
			frozenRequest,
			response,
			downstream,
			ledger,
			contentPath,
			decodedContent,
			captured,
			transformTurn,
		)
	}
	envelope, err := NewResponseEnvelope(
		mode,
		response.StatusCode,
		response.Header,
	)
	if err != nil {
		return newFailure(
			ReasonProviderResponseInvalid,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if err := downstream.Begin(ctx, envelope); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if err := ledger.RecordOrdinaryHeaders(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	contentDecoder := newOriginalContentDecoder(
		contentPath,
		decodedContent,
		mode,
		response.Header.Get("Content-Encoding"),
	)
	buffer := make([]byte, streamReadBufferBytes)
	for {
		count, readErr := response.Body.Read(buffer)
		if count > 0 {
			committed, writeErr := writeDownstream(
				ctx,
				downstream,
				buffer[:count],
			)
			if committed > 0 {
				contentDecoder.Feed(ctx, buffer[:committed])
				if err := ledger.RecordSemanticWrite(committed); err != nil {
					return newFailure(
						ReasonDownstreamCommitFailed,
						request.exchangeID,
						response.StatusCode,
						err,
					)
				}
			}
			if writeErr != nil {
				return newFailure(
					ReasonDownstreamCommitFailed,
					request.exchangeID,
					response.StatusCode,
					writeErr,
				)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			// Some authenticated CLI transports close their request context as soon
			// as they have consumed a complete SSE terminal event. net/http can then
			// report context.Canceled instead of the otherwise-immediate EOF. Treat
			// that as complete only when the bounded semantic decoder can prove the
			// provider terminal was already present in the exact bytes delivered.
			if mode == ResponseModeEventStream && errors.Is(readErr, context.Canceled) {
				if finishCanceledOriginalStream(
					ctx,
					response.Body,
					nil,
					contentDecoder,
					captured,
				) {
					break
				}
			}
			return newFailure(
				ReasonProviderResponseInvalid,
				request.exchangeID,
				response.StatusCode,
				readErr,
			)
		}
	}
	if err := ledger.RecordTerminal(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return newProviderStatusFailure(
			request.exchangeID,
			response.StatusCode,
			ProviderFieldUnknown,
		)
	}
	if decodedResponse := contentDecoder.Finish(ctx); decodedResponse != nil {
		captured.response = decodedResponse
	}
	return nil
}

func (pipeline *Pipeline) executeTransformedOriginalComplete(
	ctx context.Context,
	request ClientRequest,
	frozenRequest providertransport.Request,
	response *http.Response,
	downstream Downstream,
	ledger *CommitLedger,
	contentPath *protocolpath.Path,
	decodedContent *protocolcore.Request,
	captured *contentCapture,
	transformTurn *messagetransform.PipelineTurn,
) error {
	body, err := readBounded(response.Body, maxCompleteResponseBytes)
	if err != nil {
		return newFailure(
			ReasonProviderResponseInvalid,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	transformed, transformInput, protectedHeaders, err := applyResponseMessageTransform(
		ctx,
		transformTurn,
		response.StatusCode,
		response.Header,
		body,
	)
	if err != nil {
		return newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	pipeline.observeMessageTransformResponse(ctx, frozenRequest, transformInput)
	restoreCredentialHeaders(transformed.Headers, protectedHeaders)
	envelope, err := NewResponseEnvelope(
		ResponseModeJSON,
		response.StatusCode,
		transformed.Headers,
	)
	if err != nil {
		return newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if err := downstream.Begin(ctx, envelope); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if err := ledger.RecordOrdinaryHeaders(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	contentDecoder := newOriginalContentDecoder(
		contentPath,
		decodedContent,
		ResponseModeJSON,
		transformed.Headers.Get("Content-Encoding"),
	)
	committed, writeErr := writeDownstream(ctx, downstream, transformed.Body)
	if committed > 0 {
		contentDecoder.Feed(ctx, transformed.Body[:committed])
		if err := ledger.RecordSemanticWrite(committed); err != nil {
			return newFailure(
				ReasonDownstreamCommitFailed,
				request.exchangeID,
				response.StatusCode,
				err,
			)
		}
	}
	if writeErr != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			writeErr,
		)
	}
	if err := ledger.RecordTerminal(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return newProviderStatusFailure(
			request.exchangeID,
			response.StatusCode,
			ProviderFieldUnknown,
		)
	}
	if decodedResponse := contentDecoder.Finish(ctx); decodedResponse != nil {
		captured.response = decodedResponse
	}
	return nil
}

func (pipeline *Pipeline) executeTransformedOriginalStream(
	ctx context.Context,
	request ClientRequest,
	frozenRequest providertransport.Request,
	response *http.Response,
	downstream Downstream,
	ledger *CommitLedger,
	contentPath *protocolpath.Path,
	decodedContent *protocolcore.Request,
	captured *contentCapture,
	transformTurn *messagetransform.PipelineTurn,
) error {
	logicalBody, err := newLogicalTransformStream(
		response.Body,
		response.Header.Get("Content-Encoding"),
	)
	if err != nil {
		return newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	defer logicalBody.Close()
	transformer, err := newStreamMessageTransformer(
		transformTurn,
		response.StatusCode,
		response.Header,
	)
	if err != nil {
		return newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	contentDecoder := newOriginalContentDecoder(
		contentPath,
		decodedContent,
		ResponseModeEventStream,
		"",
	)
	started := false
	buffer := make([]byte, streamReadBufferBytes)
	for {
		count, readErr := logicalBody.Read(buffer)
		if count > 0 {
			transformed, headerReady, transformErr := transformer.Feed(
				ctx,
				buffer[:count],
			)
			if transformErr != nil {
				return newFailure(
					ReasonMessageTransformFailed,
					request.exchangeID,
					response.StatusCode,
					transformErr,
				)
			}
			if headerReady {
				envelope, envelopeErr := transformer.OriginalEnvelope()
				if envelopeErr != nil {
					return newFailure(
						ReasonMessageTransformFailed,
						request.exchangeID,
						response.StatusCode,
						envelopeErr,
					)
				}
				if beginErr := downstream.Begin(ctx, envelope); beginErr != nil {
					return newFailure(
						ReasonDownstreamCommitFailed,
						request.exchangeID,
						response.StatusCode,
						beginErr,
					)
				}
				if recordErr := ledger.RecordOrdinaryHeaders(); recordErr != nil {
					return newFailure(
						ReasonDownstreamCommitFailed,
						request.exchangeID,
						response.StatusCode,
						recordErr,
					)
				}
				started = true
			}
			if len(transformed) > 0 {
				committed, writeErr := writeDownstream(ctx, downstream, transformed)
				if committed > 0 {
					contentDecoder.Feed(ctx, transformed[:committed])
					if recordErr := ledger.RecordSemanticWrite(committed); recordErr != nil {
						return newFailure(
							ReasonDownstreamCommitFailed,
							request.exchangeID,
							response.StatusCode,
							recordErr,
						)
					}
				}
				if writeErr != nil {
					return newFailure(
						ReasonDownstreamCommitFailed,
						request.exchangeID,
						response.StatusCode,
						writeErr,
					)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) && finishCanceledOriginalStream(
				ctx,
				logicalBody,
				transformer,
				contentDecoder,
				captured,
			) {
				break
			}
			return newFailure(
				ReasonProviderResponseInvalid,
				request.exchangeID,
				response.StatusCode,
				readErr,
			)
		}
		if count == 0 {
			return newFailure(
				ReasonProviderResponseInvalid,
				request.exchangeID,
				response.StatusCode,
				io.ErrNoProgress,
			)
		}
	}
	if err := transformer.Finish(); err != nil {
		return newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	pipeline.observeMessageTransformStreamResponse(ctx, frozenRequest, transformer)
	if !started {
		return newFailure(
			ReasonMessageTransformFailed,
			request.exchangeID,
			response.StatusCode,
			errors.New("streaming response contained no transformable event"),
		)
	}
	if err := ledger.RecordTerminal(); err != nil {
		return newFailure(
			ReasonDownstreamCommitFailed,
			request.exchangeID,
			response.StatusCode,
			err,
		)
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return newProviderStatusFailure(
			request.exchangeID,
			response.StatusCode,
			ProviderFieldUnknown,
		)
	}
	if decodedResponse := contentDecoder.Finish(ctx); decodedResponse != nil {
		captured.response = decodedResponse
	}
	return nil
}

func finishCanceledOriginalStream(
	ctx context.Context,
	body io.Reader,
	transformer *streamMessageTransformer,
	contentDecoder *originalContentDecoder,
	captured *contentCapture,
) bool {
	if transformer != nil && transformer.Finish() != nil {
		return false
	}
	decodedResponse := contentDecoder.Finish(context.WithoutCancel(ctx))
	if decodedResponse == nil {
		return false
	}
	captured.response = decodedResponse
	if terminalBody, ok := body.(interface {
		ConfirmSemanticTerminal()
	}); ok {
		terminalBody.ConfirmSemanticTerminal()
	}
	return true
}

type originalContentDecoder struct {
	path       *protocolpath.Path
	request    *protocolcore.Request
	mode       ResponseMode
	stream     protocolpath.Stream
	body       bytes.Buffer
	compressed bool
	failed     bool
	finished   bool
	response   *protocolcore.Response
}

func reportDeepProtocolFailure(_ string, _ error) {
	// Semantic inspection is deliberately best-effort on an original-wire
	// passthrough. The exact request and response remain authoritative; callers
	// already mark the projection unavailable when this decoder fails.
}

func newOriginalContentDecoder(
	path *protocolpath.Path,
	request *protocolcore.Request,
	mode ResponseMode,
	contentEncoding string,
) *originalContentDecoder {
	decoder := &originalContentDecoder{path: path, request: request, mode: mode}
	if path == nil || request == nil {
		decoder.failed = true
		return decoder
	}
	encoding := strings.TrimSpace(contentEncoding)
	switch {
	case encoding == "" || strings.EqualFold(encoding, "identity"):
	case strings.EqualFold(encoding, "gzip"):
		decoder.compressed = true
	default:
		decoder.failed = true
		return decoder
	}
	if mode == ResponseModeEventStream {
		stream, err := path.Streaming().NewStream(request.Clone())
		if err != nil {
			decoder.failed = true
			return decoder
		}
		decoder.stream = stream
	}
	return decoder
}

func (decoder *originalContentDecoder) Feed(ctx context.Context, fragment []byte) {
	if decoder == nil || decoder.failed || len(fragment) == 0 {
		return
	}
	if decoder.compressed {
		if decoder.body.Len()+len(fragment) > maxCompleteResponseBytes {
			decoder.failed = true
			decoder.body.Reset()
			return
		}
		_, _ = decoder.body.Write(fragment)
		return
	}
	if decoder.mode == ResponseModeEventStream {
		if _, err := decoder.stream.Feed(ctx, fragment); err != nil {
			reportDeepProtocolFailure("stream_feed", err)
			decoder.failed = true
		}
		return
	}
	if decoder.body.Len()+len(fragment) > maxCompleteResponseBytes {
		decoder.failed = true
		decoder.body.Reset()
		return
	}
	_, _ = decoder.body.Write(fragment)
}

func (decoder *originalContentDecoder) Finish(ctx context.Context) *protocolcore.Response {
	if decoder == nil {
		return nil
	}
	if decoder.finished {
		if decoder.response == nil {
			return nil
		}
		cloned := decoder.response.Clone()
		return &cloned
	}
	decoder.finished = true
	if decoder.failed || decoder.path == nil || decoder.request == nil {
		return nil
	}
	if decoder.compressed {
		reader, err := gzip.NewReader(bytes.NewReader(decoder.body.Bytes()))
		if err != nil {
			return nil
		}
		decoded, err := readBounded(reader, maxCompleteResponseBytes)
		closeErr := reader.Close()
		if err != nil || closeErr != nil {
			return nil
		}
		if decoder.mode == ResponseModeEventStream {
			if _, err := decoder.stream.Feed(ctx, decoded); err != nil {
				return nil
			}
		} else {
			decoder.body.Reset()
			_, _ = decoder.body.Write(decoded)
		}
	}
	if decoder.mode == ResponseModeEventStream {
		terminal, err := decoder.stream.FinishDecoded(ctx)
		if err != nil {
			reportDeepProtocolFailure("stream_finish", err)
			return nil
		}
		response := terminal.DecodedResponse().Clone()
		_, _ = terminal.Approve()
		decoder.response = &response
		cloned := response.Clone()
		return &cloned
	}
	response, _, err := decoder.path.Backend().DecodeResponse(
		decoder.request.Clone(),
		decoder.body.Bytes(),
	)
	if err != nil {
		reportDeepProtocolFailure("complete_response", err)
		return nil
	}
	cloned := response.Clone()
	decoder.response = &cloned
	result := cloned.Clone()
	return &result
}

func (pipeline *Pipeline) executeStream(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	protocolPath *protocolpath.Path,
	decoded protocolcore.Request,
	frozenRequest providertransport.Request,
	downstream Downstream,
	ledger *CommitLedger,
	result *Result,
	captured *contentCapture,
	transformTurn *messagetransform.PipelineTurn,
) error {
	if !transformTurn.HasResponse() {
		if err := downstream.Begin(
			ctx,
			managedResponseEnvelope(ResponseModeEventStream),
		); err != nil {
			return newFailure(
				ReasonDownstreamCommitFailed,
				request.exchangeID,
				0,
				err,
			)
		}
		if err := ledger.RecordHoldEnvelope(); err != nil {
			return newFailure(
				ReasonDownstreamCommitFailed,
				request.exchangeID,
				0,
				err,
			)
		}
	}

	var retryDeadline time.Time
	for {
		if err := ledger.RecordUpstreamSend(int64(len(frozenRequest.Body()))); err != nil {
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				newFailure(
					ReasonProviderRequestInvalid,
					request.exchangeID,
					0,
					err,
				),
			)
		}
		response, evidence, sendErr := pipeline.provider.Do(ctx, frozenRequest)
		result.Credential = evidence.Credential
		if evidence.Presentation.RequestedRef != "" {
			result.Presentation = evidence.Presentation
		}
		result.Transport = evidence.Transport
		if sendErr != nil {
			failure := pipeline.classifyProviderError(
				ctx,
				request.exchangeID,
				sendErr,
			)
			if pipeline.canRetryFailure(
				sendErr,
				0,
				request,
				ledger,
				result.TransportResends,
			) {
				if retryDeadline.IsZero() {
					retryDeadline = pipeline.now().Add(pipeline.hold.MaxDuration)
				}
				if err := pipeline.waitForRetry(
					ctx,
					request.exchangeID,
					result.TransportResends+1,
					0,
					retryDeadline,
				); err == nil {
					result.TransportResends++
					continue
				} else {
					failure = pipeline.classifyRetryWaitError(
						ctx,
						request.exchangeID,
						err,
					)
				}
			} else if pipeline.retryBudgetExhausted(
				sendErr,
				0,
				request,
				ledger,
				result.TransportResends,
			) {
				failure = newFailure(
					ReasonTransportRetryExhausted,
					request.exchangeID,
					0,
					sendErr,
				)
			}
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				failure,
			)
		}
		if response == nil || response.Body == nil {
			failure := newFailure(
				ReasonProviderTransportFailed,
				request.exchangeID,
				0,
				errors.New("provider returned an incomplete response"),
			)
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				failure,
			)
		}
		ledger.RecordUpstreamResponse()

		statusCode := response.StatusCode
		if statusCode < 200 || statusCode > 299 {
			providerField := classifyProviderRejection(response.Body)
			_ = response.Body.Close()
			failure := newProviderStatusFailure(
				request.exchangeID,
				statusCode,
				providerField,
			)
			if pipeline.canRetryFailure(
				nil,
				statusCode,
				request,
				ledger,
				result.TransportResends,
			) {
				if retryDeadline.IsZero() {
					retryDeadline = pipeline.now().Add(pipeline.hold.MaxDuration)
				}
				if err := pipeline.waitForRetry(
					ctx,
					request.exchangeID,
					result.TransportResends+1,
					statusCode,
					retryDeadline,
				); err == nil {
					result.TransportResends++
					continue
				} else {
					failure = pipeline.classifyRetryWaitError(
						ctx,
						request.exchangeID,
						err,
					)
				}
			} else if pipeline.retryBudgetExhausted(
				nil,
				statusCode,
				request,
				ledger,
				result.TransportResends,
			) {
				failure = newFailure(
					ReasonTransportRetryExhausted,
					request.exchangeID,
					statusCode,
					errors.New("provider retry status exhausted the resend budget"),
				)
			}
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				failure,
			)
		}
		if !contentTypeMatches(
			response.Header.Get("Content-Type"),
			"text/event-stream",
		) {
			failure := newProviderContentTypeFailure(
				request.exchangeID,
				statusCode,
				response.Body,
				"text/event-stream",
			)
			_ = response.Body.Close()
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				failure,
			)
		}
		transformer, err := newStreamMessageTransformer(
			transformTurn,
			statusCode,
			response.Header,
		)
		if err != nil {
			_ = response.Body.Close()
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				newFailure(
					ReasonMessageTransformFailed,
					request.exchangeID,
					statusCode,
					err,
				),
			)
		}
		streamBody := response.Body
		if transformer != nil {
			streamBody, err = newLogicalTransformStream(
				response.Body,
				response.Header.Get("Content-Encoding"),
			)
			if err != nil {
				_ = response.Body.Close()
				return pipeline.failStream(
					ctx,
					request.exchangeID,
					downstream,
					ledger,
					newFailure(
						ReasonMessageTransformFailed,
						request.exchangeID,
						statusCode,
						err,
					),
				)
			}
		}

		stream, err := protocolPath.Streaming().NewStream(decoded)
		if err != nil {
			_ = streamBody.Close()
			return pipeline.failStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				newFailure(
					ReasonProviderResponseInvalid,
					request.exchangeID,
					statusCode,
					err,
				),
			)
		}
		streamErr := pipeline.consumeProviderStream(
			ctx,
			request,
			selection,
			decoded,
			stream,
			streamBody,
			downstream,
			ledger,
			result,
			captured,
			transformer,
			frozenRequest,
		)
		if streamErr == nil {
			return nil
		}
		if pipeline.canRetryFailure(
			streamErr,
			0,
			request,
			ledger,
			result.TransportResends,
		) {
			if retryDeadline.IsZero() {
				retryDeadline = pipeline.now().Add(pipeline.hold.MaxDuration)
			}
			if err := pipeline.waitForRetry(
				ctx,
				request.exchangeID,
				result.TransportResends+1,
				0,
				retryDeadline,
			); err == nil {
				result.TransportResends++
				continue
			} else {
				streamErr = pipeline.classifyRetryWaitError(
					ctx,
					request.exchangeID,
					err,
				)
			}
		}
		failure := pipeline.classifyStreamError(
			ctx,
			request.exchangeID,
			statusCode,
			streamErr,
		)
		if pipeline.retryBudgetExhausted(
			streamErr,
			0,
			request,
			ledger,
			result.TransportResends,
		) {
			failure = newFailure(
				ReasonTransportRetryExhausted,
				request.exchangeID,
				0,
				streamErr,
			)
		}
		return pipeline.failStream(
			ctx,
			request.exchangeID,
			downstream,
			ledger,
			failure,
		)
	}
}

func (pipeline *Pipeline) consumeProviderStream(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	decoded protocolcore.Request,
	stream protocolpath.Stream,
	body io.ReadCloser,
	downstream Downstream,
	ledger *CommitLedger,
	result *Result,
	captured *contentCapture,
	transformer *streamMessageTransformer,
	frozenRequest providertransport.Request,
) error {
	readContext, cancelRead := context.WithCancelCause(ctx)
	readResults := make(chan providerReadResult)
	readDone := make(chan struct{})
	go pumpProviderBody(readContext, body, readResults, readDone)
	defer func() {
		cancelRead(context.Canceled)
		_ = body.Close()
		<-readDone
	}()

	providerIdle := time.NewTimer(
		pipeline.streamBudgets.ProviderProgressTimeout,
	)
	defer providerIdle.Stop()
	keepalive := time.NewTimer(pipeline.streamBudgets.KeepaliveInterval)
	defer keepalive.Stop()

	streamComplete := false
	for !streamComplete {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-providerIdle.C:
			return ErrProviderSemanticIdle
		case <-keepalive.C:
			if ledger.Snapshot().DownstreamHoldEnvelope {
				if err := downstream.Keepalive(ctx); err != nil {
					return newFailure(
						ReasonDownstreamDisconnected,
						request.exchangeID,
						0,
						err,
					)
				}
			}
			resetTimer(keepalive, pipeline.streamBudgets.KeepaliveInterval)
		case result, open := <-readResults:
			if !open {
				streamComplete = true
				continue
			}
			fragment := result.fragment
			if len(fragment) > 0 && transformer != nil {
				transformed, headerReady, transformErr := transformer.Feed(ctx, fragment)
				if transformErr != nil {
					return newFailure(
						ReasonMessageTransformFailed,
						request.exchangeID,
						0,
						transformErr,
					)
				}
				fragment = transformed
				if headerReady {
					envelope, envelopeErr := transformer.Envelope()
					if envelopeErr != nil {
						return newFailure(
							ReasonMessageTransformFailed,
							request.exchangeID,
							0,
							envelopeErr,
						)
					}
					if beginErr := downstream.Begin(ctx, envelope); beginErr != nil {
						return newFailure(
							ReasonDownstreamCommitFailed,
							request.exchangeID,
							0,
							beginErr,
						)
					}
					if recordErr := ledger.RecordHoldEnvelope(); recordErr != nil {
						return newFailure(
							ReasonDownstreamCommitFailed,
							request.exchangeID,
							0,
							recordErr,
						)
					}
				}
			}
			before := stream.SemanticProgress()
			if len(fragment) > 0 {
				safe, decodeErr := stream.Feed(ctx, fragment)
				if stream.SemanticProgress() > before {
					resetTimer(
						providerIdle,
						pipeline.streamBudgets.ProviderProgressTimeout,
					)
				}
				if len(safe) > 0 {
					committed, writeErr := writeDownstream(ctx, downstream, safe)
					if committed > 0 {
						if err := ledger.RecordSemanticWrite(committed); err != nil {
							return err
						}
						resetTimer(
							keepalive,
							pipeline.streamBudgets.KeepaliveInterval,
						)
					}
					if writeErr != nil {
						return newFailure(
							ReasonDownstreamCommitFailed,
							request.exchangeID,
							0,
							writeErr,
						)
					}
				}
				if decodeErr != nil {
					return decodeErr
				}
			}
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					streamComplete = true
					continue
				}
				return result.err
			}
			if len(result.fragment) == 0 {
				return io.ErrNoProgress
			}
		}
	}
	if transformer != nil {
		if err := transformer.Finish(); err != nil {
			return newFailure(
				ReasonMessageTransformFailed,
				request.exchangeID,
				0,
				err,
			)
		}
		pipeline.observeMessageTransformStreamResponse(ctx, frozenRequest, transformer)
	}

	terminal, err := stream.FinishDecoded(ctx)
	if err != nil {
		return err
	}
	result.Translation = result.Translation.Merge(
		terminal.TranslationReport(),
	)
	intents := terminal.ToolIntents()
	if err := pipeline.decideStreamTools(
		ctx,
		request,
		selection,
		decoded,
		intents,
		downstream,
	); err != nil {
		_ = terminal.Reject()
		return err
	}
	release, err := terminal.Approve()
	if err != nil {
		return err
	}
	// A compatible text-only stream is released incrementally as each event
	// becomes safe. Its terminal approval therefore has no buffered bytes to
	// publish. Empty release is a successful terminal state, not a failed
	// downstream write. Tool-bearing streams still enter this branch with the
	// bytes held behind approval.
	if len(release) > 0 {
		committed, writeErr := writeDownstream(ctx, downstream, release)
		if committed > 0 {
			if err := ledger.RecordSemanticWrite(committed); err != nil {
				return err
			}
			if len(intents) > 0 {
				if err := ledger.RecordToolExposure(toolKeys(intents)); err != nil {
					return err
				}
			}
		}
		if writeErr != nil {
			return newFailure(
				ReasonDownstreamCommitFailed,
				request.exchangeID,
				0,
				writeErr,
			)
		}
	}
	if err := ledger.RecordTerminal(); err != nil {
		return err
	}
	responseForEvidence := terminal.DecodedResponse().Clone()
	captured.response = &responseForEvidence
	return nil
}

type providerReadResult struct {
	fragment []byte
	err      error
}

func pumpProviderBody(
	ctx context.Context,
	body io.Reader,
	results chan<- providerReadResult,
	done chan<- struct{},
) {
	defer close(done)
	defer close(results)
	buffer := make([]byte, streamReadBufferBytes)
	for {
		count, err := body.Read(buffer)
		result := providerReadResult{err: err}
		if count > 0 {
			result.fragment = bytes.Clone(buffer[:count])
		}
		select {
		case results <- result:
		case <-ctx.Done():
			return
		}
		if err != nil || count == 0 {
			return
		}
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func (pipeline *Pipeline) decideTools(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	decoded protocolcore.Request,
	intents []protocolcore.ToolIntent,
) error {
	if len(intents) == 0 {
		return nil
	}
	workspaceRoot := ""
	structuredWorkspaceTools := false
	if admission, available := request.CaptureAdmission(); available {
		workspaceRoot, _ = admission.WorkspaceRoot()
		structuredWorkspaceTools = admission.Supports(
			clientadapter.FeatureStructuredWorkspaceTools,
		)
	}
	decisionContext, err := NewToolDecisionContext(
		selection.policySet,
		workspaceRoot,
		structuredWorkspaceTools,
		decoded.Tools,
		decoded.ToolNamespaces,
	)
	if err != nil {
		return newFailure(
			ReasonToolDecisionUnavailable,
			request.exchangeID,
			0,
			err,
		)
	}
	decisionRequest, err := NewToolDecisionRequest(
		request.exchangeID,
		selection.environmentID,
		selection.environmentRevision,
		selection.environmentDigest,
		selection.routeID,
		selection.routeRevision,
		decisionContext,
		intents,
	)
	if err != nil {
		return newFailure(
			ReasonToolDecisionUnavailable,
			request.exchangeID,
			0,
			err,
		)
	}
	decision, err := pipeline.toolDecisions.Decide(ctx, decisionRequest)
	if err != nil {
		return newFailure(
			ReasonToolDecisionUnavailable,
			request.exchangeID,
			0,
			err,
		)
	}
	if err := decision.validate(); err != nil {
		return newFailure(
			ReasonToolDecisionUnavailable,
			request.exchangeID,
			0,
			err,
		)
	}
	if decision.Outcome == ToolDecisionRejected {
		reason := ReasonToolDecisionRejected
		if decision.ReasonCode == "approval_expired" {
			reason = ReasonToolDecisionExpired
		}
		return newFailure(
			reason,
			request.exchangeID,
			0,
			errors.Join(
				ErrToolRejected,
				fmt.Errorf("reasonCode=%s", decision.ReasonCode),
			),
		)
	}
	return nil
}

// decideStreamTools keeps the already-open client stream alive while a
// complete tool-intent group is waiting for a human decision. The provider's
// tool bytes remain held by the terminal release boundary; keepalives contain
// no semantic payload and cannot expose a tool before approval.
func (pipeline *Pipeline) decideStreamTools(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	decoded protocolcore.Request,
	intents []protocolcore.ToolIntent,
	downstream Downstream,
) error {
	if len(intents) == 0 {
		return nil
	}
	decisionContext, cancelDecision := context.WithCancel(ctx)
	defer cancelDecision()
	result := make(chan error, 1)
	go func() {
		result <- pipeline.decideTools(
			decisionContext,
			request,
			selection,
			decoded,
			intents,
		)
	}()
	keepalive := time.NewTimer(pipeline.streamBudgets.KeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case err := <-result:
			return err
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-keepalive.C:
			if err := downstream.Keepalive(ctx); err != nil {
				cancelDecision()
				return newFailure(
					ReasonDownstreamDisconnected,
					request.exchangeID,
					0,
					err,
				)
			}
			resetTimer(keepalive, pipeline.streamBudgets.KeepaliveInterval)
		}
	}
}

func (pipeline *Pipeline) canRetryFailure(
	err error,
	statusCode int,
	request ClientRequest,
	ledger *CommitLedger,
	resends uint32,
) bool {
	retryable := retryableStatus(statusCode) || retryableTransportError(err)
	if !retryable || resends >= pipeline.hold.MaxTransportResends {
		return false
	}
	allowed, _ := ledger.CanTransportResend(
		request.replayClass,
		pipeline.hold.AllowResendAfterProviderResponse,
	)
	return allowed
}

func (pipeline *Pipeline) retryBudgetExhausted(
	err error,
	statusCode int,
	request ClientRequest,
	ledger *CommitLedger,
	resends uint32,
) bool {
	if resends < pipeline.hold.MaxTransportResends ||
		(!retryableStatus(statusCode) && !retryableTransportError(err)) {
		return false
	}
	allowed, _ := ledger.CanTransportResend(
		request.replayClass,
		pipeline.hold.AllowResendAfterProviderResponse,
	)
	return allowed
}

func (pipeline *Pipeline) waitForRetry(
	ctx context.Context,
	exchangeID string,
	ordinal uint32,
	statusCode int,
	deadline time.Time,
) error {
	if !pipeline.now().Before(deadline) {
		return context.DeadlineExceeded
	}
	waitContext, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	return pipeline.retryWaiter.WaitForRetry(
		waitContext,
		RetryObservation{
			ExchangeID:    exchangeID,
			ResendOrdinal: ordinal,
			StatusCode:    statusCode,
			Backoff:       pipeline.hold.RetryDelay,
		},
	)
}

func (pipeline *Pipeline) abortStream(
	ctx context.Context,
	exchangeID string,
	downstream Downstream,
	ledger *CommitLedger,
	failure error,
) error {
	var typed *Failure
	if !errors.As(failure, &typed) {
		typed = newFailure(
			ReasonProviderResponseInvalid,
			exchangeID,
			0,
			failure,
		)
	}
	if errors.Is(failure, context.Canceled) ||
		errors.Is(failure, context.DeadlineExceeded) ||
		errors.Is(context.Cause(ctx), ErrRuntimeStopping) {
		return typed
	}
	abortErr := downstream.Abort(ctx, FailureNotice{
		ReasonCode:     typed.Code,
		ProviderStatus: typed.ProviderStatus,
		ProviderField:  typed.ProviderField,
		ProtocolReason: typed.ProtocolReason,
		ResponseIssue:  typed.ResponseIssue,
	})
	if abortErr != nil {
		return errors.Join(
			typed,
			newFailure(
				ReasonDownstreamFailureAborted,
				exchangeID,
				0,
				abortErr,
			),
		)
	}
	if err := ledger.RecordFailure(); err != nil {
		return errors.Join(
			typed,
			newFailure(
				ReasonDownstreamFailureAborted,
				exchangeID,
				0,
				err,
			),
		)
	}
	return typed
}

func (pipeline *Pipeline) failStream(
	ctx context.Context,
	exchangeID string,
	downstream Downstream,
	ledger *CommitLedger,
	failure error,
) error {
	if !ledger.Snapshot().DownstreamHoldEnvelope {
		return failure
	}
	return pipeline.abortStream(ctx, exchangeID, downstream, ledger, failure)
}

func classifyProviderRejection(reader io.Reader) ProviderField {
	if reader == nil {
		return ProviderFieldUnknown
	}
	body, err := readBounded(reader, maxProviderErrorBytes)
	if err != nil {
		return ProviderFieldUnknown
	}
	var payload struct {
		Error struct {
			Param string `json:"param"`
		} `json:"error"`
		Detail []struct {
			Location []json.RawMessage `json:"loc"`
		} `json:"detail"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return ProviderFieldUnknown
	}
	if field := knownProviderField(payload.Error.Param); field != ProviderFieldUnknown {
		return field
	}
	for _, detail := range payload.Detail {
		for index := len(detail.Location) - 1; index >= 0; index-- {
			var fieldName string
			if json.Unmarshal(detail.Location[index], &fieldName) != nil {
				continue
			}
			if field := knownProviderField(fieldName); field != ProviderFieldUnknown {
				return field
			}
		}
	}
	return ProviderFieldUnknown
}

func newProviderContentTypeFailure(
	exchangeID string,
	providerStatus int,
	body io.Reader,
	expected string,
) *Failure {
	failure := newFailure(
		ReasonProviderResponseInvalid,
		exchangeID,
		providerStatus,
		protocolcore.NewFailure(
			protocolcore.ReasonInvalidProviderResponse,
			"$.http.content_type",
			fmt.Errorf("provider response Content-Type is not %s", expected),
		),
	)
	failure.ProviderField = classifyProviderRejection(body)
	failure.ResponseIssue = ProviderResponseIssueContentType
	return failure
}

func (pipeline *Pipeline) classifyProviderError(
	ctx context.Context,
	exchangeID string,
	err error,
) *Failure {
	if errors.Is(context.Cause(ctx), ErrRuntimeStopping) ||
		errors.Is(err, ErrRuntimeStopping) {
		return newFailure(ReasonExchangeRuntimeStopping, exchangeID, 0, err)
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		ctx.Err() != nil {
		return newFailure(ReasonExchangeCanceled, exchangeID, 0, err)
	}
	if errors.Is(err, secretstore.ErrNotFound) ||
		errors.Is(err, secretstore.ErrLocked) ||
		errors.Is(err, secretstore.ErrDenied) ||
		errors.Is(err, secretstore.ErrUnavailable) ||
		errors.Is(err, secretstore.ErrReadOnly) {
		return newFailure(
			ReasonProviderCredentialUnavailable,
			exchangeID,
			0,
			err,
		)
	}
	return newFailure(ReasonProviderTransportFailed, exchangeID, 0, err)
}

func (pipeline *Pipeline) classifyRetryWaitError(
	ctx context.Context,
	exchangeID string,
	err error,
) *Failure {
	if errors.Is(context.Cause(ctx), ErrRuntimeStopping) ||
		errors.Is(err, ErrRuntimeStopping) {
		return newFailure(ReasonExchangeRuntimeStopping, exchangeID, 0, err)
	}
	if ctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded) {
		return newFailure(ReasonExchangeCanceled, exchangeID, 0, err)
	}
	return newFailure(ReasonTransportRetryExhausted, exchangeID, 0, err)
}

func (pipeline *Pipeline) classifyStreamError(
	ctx context.Context,
	exchangeID string,
	providerStatus int,
	err error,
) *Failure {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	if errors.Is(context.Cause(ctx), ErrRuntimeStopping) ||
		errors.Is(err, ErrRuntimeStopping) {
		return newFailure(
			ReasonExchangeRuntimeStopping,
			exchangeID,
			providerStatus,
			err,
		)
	}
	if ctx.Err() != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return newFailure(
			ReasonExchangeCanceled,
			exchangeID,
			providerStatus,
			err,
		)
	}
	if errors.Is(err, providertransport.ErrProviderResponseIdle) ||
		errors.Is(err, ErrProviderSemanticIdle) {
		return newFailure(
			ReasonProviderResponseIdle,
			exchangeID,
			providerStatus,
			err,
		)
	}
	if retryableTransportError(err) {
		return newFailure(
			ReasonProviderTransportFailed,
			exchangeID,
			providerStatus,
			err,
		)
	}
	if protocolcore.ReasonOf(err) != "" {
		return newFailure(
			ReasonProviderResponseInvalid,
			exchangeID,
			providerStatus,
			err,
		)
	}
	return newFailure(
		ReasonProviderResponseInvalid,
		exchangeID,
		providerStatus,
		err,
	)
}

func (pipeline *Pipeline) begin(
	clientContext context.Context,
	actionID string,
) (
	context.Context,
	*operation,
	*offlinehold.ActionLease,
	error,
) {
	if err := clientContext.Err(); err != nil {
		return nil, nil, nil, context.Cause(clientContext)
	}
	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if pipeline.closing || pipeline.ownerContext.Err() != nil {
		return nil, nil, nil, ErrRuntimeStopping
	}
	operationContext, cancel := context.WithCancelCause(pipeline.ownerContext)
	active := &operation{cancel: cancel}
	active.stopClient = context.AfterFunc(clientContext, func() {
		cancel(context.Cause(clientContext))
	})
	action, err := pipeline.actions.BeginAction(
		operationContext,
		offlinehold.ActionRequest{ActionID: actionID},
	)
	if err != nil {
		active.stopClient()
		cancel(err)
		return nil, nil, nil, fmt.Errorf(
			"%w: %w",
			errOfflineHoldAdmission,
			err,
		)
	}
	pipeline.operations[active] = struct{}{}
	return operationContext, active, action, nil
}

func (pipeline *Pipeline) finish(active *operation) {
	active.stopClient()
	active.cancel(nil)
	pipeline.mu.Lock()
	delete(pipeline.operations, active)
	pipeline.signalLocked()
	pipeline.mu.Unlock()
}

// BeginShutdown atomically rejects new Exchanges and cancels every active
// operation. ProductRuntime calls this before shutting down the provider so a
// provider-owned response body can be closed before the Exchange drain waits.
func (pipeline *Pipeline) BeginShutdown() {
	pipeline.mu.Lock()
	pipeline.closing = true
	pipeline.cancelOwner(ErrRuntimeStopping)
	for active := range pipeline.operations {
		active.cancel(ErrRuntimeStopping)
	}
	pipeline.mu.Unlock()
}

// Drain waits for active Exchanges after their blocking provider resources
// have been asked to close. A later call may retry after an earlier deadline.
func (pipeline *Pipeline) Drain(ctx context.Context) error {
	if ctx == nil {
		return errors.New("Exchange pipeline drain context is nil")
	}
	pipeline.mu.Lock()
	for len(pipeline.operations) != 0 {
		changed := pipeline.changed
		pipeline.mu.Unlock()
		select {
		case <-ctx.Done():
			return fmt.Errorf("drain Exchange operations: %w", ctx.Err())
		case <-changed:
		}
		pipeline.mu.Lock()
	}
	pipeline.mu.Unlock()
	return nil
}

// Shutdown is the standalone lifecycle boundary. ProductRuntime uses the split
// BeginShutdown and Drain phases to place provider-body closure between them.
func (pipeline *Pipeline) Shutdown(ctx context.Context) error {
	pipeline.BeginShutdown()
	return pipeline.Drain(ctx)
}

func (pipeline *Pipeline) signalLocked() {
	close(pipeline.changed)
	pipeline.changed = make(chan struct{})
}

func writeDownstream(
	ctx context.Context,
	downstream Downstream,
	body []byte,
) (int, error) {
	if len(body) == 0 {
		return 0, errors.New("downstream write body is empty")
	}
	count, err := downstream.Write(ctx, body)
	if count < 0 || count > len(body) {
		return 0, errors.New("downstream returned an invalid committed byte count")
	}
	if err == nil && count != len(body) {
		return count, io.ErrShortWrite
	}
	return count, err
}

func readBounded(reader io.Reader, limit int) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > limit {
		return nil, errors.New("provider response body has an invalid size")
	}
	return body, nil
}

func contentTypeMatches(value, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && strings.EqualFold(mediaType, expected)
}

func retryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableTransportError(err error) bool {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, providertransport.ErrRedirectNotAllowed) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, ErrProviderSemanticIdle) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func responseToolIntents(
	response protocolcore.Response,
) []protocolcore.ToolIntent {
	intents := make([]protocolcore.ToolIntent, 0)
	for _, block := range response.Blocks {
		if block.Kind != protocolcore.BlockToolCall {
			continue
		}
		intents = append(intents, protocolcore.ToolIntent{
			ResponseID: response.ID,
			Ordinal:    len(intents),
			Call:       block.ToolCall.Clone(),
		})
	}
	return intents
}

func toolKeys(intents []protocolcore.ToolIntent) []string {
	keys := make([]string, len(intents))
	for index, intent := range intents {
		keys[index] = intent.Call.Key.Source() + ":" + intent.Call.Key.WireID()
	}
	return keys
}
