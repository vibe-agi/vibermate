package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
	"github.com/vibe-agi/vibermate/internal/providertransport"
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

var errOfflineHoldAdmission = errors.New(
	"offline-hold Exchange admission failed",
)

// Pipeline owns all active Exchange contexts. It has no listener and cannot be
// reached without an ingress component explicitly receiving its Executor.
type Pipeline struct {
	resolver      access.SnapshotResolver
	actions       offlinehold.ActionAdmission
	protocolPaths *protocolpath.Selector
	provider      Provider
	toolDecisions ToolDecisionGate
	retryWaiter   RetryWaiter
	observer      AttemptObserver
	observeLimit  time.Duration
	hold          HoldPolicy
	streamBudgets StreamBudgets
	attemptIDs    AttemptIDSource

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
		options.Resolver == nil ||
		options.ProtocolPaths == nil ||
		options.Provider == nil ||
		options.ToolDecisions == nil ||
		options.RetryWaiter == nil ||
		options.Observer == nil ||
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
		resolver:      options.Resolver,
		actions:       options.Actions,
		protocolPaths: options.ProtocolPaths,
		provider:      options.Provider,
		toolDecisions: options.ToolDecisions,
		retryWaiter:   options.RetryWaiter,
		observer:      options.Observer,
		observeLimit:  options.ObservationTimeout,
		hold:          options.Hold,
		streamBudgets: options.Stream,
		attemptIDs:    attemptIDs,
		ownerContext:  ownerContext,
		cancelOwner:   cancelOwner,
		operations:    make(map[*operation]struct{}),
		changed:       make(chan struct{}),
	}, nil
}

func (pipeline *Pipeline) Execute(
	ctx context.Context,
	request ClientRequest,
	downstream Downstream,
) (result Result, resultErr error) {
	result = Result{
		ExchangeID: request.exchangeID,
		AccessID:   request.ingress.AccessID().String(),
		Outcome:    AttemptFailed,
	}
	defer func() {
		pipeline.observeAttempt(request, result, resultErr)
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

	// This is the sole resolver call for the entire Exchange. Every later stage
	// receives only the frozen value returned here.
	snapshot, err := pipeline.resolver.ResolveAccess(request.ingress.AccessID())
	if err != nil {
		return result, newFailure(
			ReasonAccessPlanUnavailable,
			request.exchangeID,
			0,
			err,
		)
	}
	result.AccessRevision = snapshot.Revision()
	result.PlanHash = snapshot.PlanHash().String()
	if err := request.ingress.ValidateSnapshot(snapshot); err != nil {
		return result, newFailure(
			ReasonIngressBindingStale,
			request.exchangeID,
			0,
			err,
		)
	}

	selection, err := selectFrozenPlan(snapshot)
	if err != nil {
		return result, newFailure(
			ReasonUnsupportedAccessPlan,
			request.exchangeID,
			0,
			err,
		)
	}
	result.RouteHost = selection.target.NetworkHost()
	result.CredentialBindingID = selection.accountID.String()
	if err := validateClientOperation(
		selection.codecPlan,
		request.operation,
		request.replayClass,
	); err != nil {
		return result, newFailure(
			ReasonUnsupportedAccessPlan,
			request.exchangeID,
			0,
			err,
		)
	}
	protocolPath, err := pipeline.protocolPaths.Select(
		selection.codecPlan,
		request.operation.id,
	)
	if err != nil {
		return result, newFailure(
			ReasonUnsupportedAccessPlan,
			request.exchangeID,
			0,
			err,
		)
	}
	decoded, clientRequestReport, err :=
		protocolPath.Client().DecodeRequest(request.body)
	result.Translation = result.Translation.Merge(clientRequestReport)
	if err != nil {
		reason := ReasonInvalidExchangeRequest
		if protocolcore.ReasonOf(err) ==
			protocolcore.ReasonUnsupportedClientInput {
			reason = ReasonUnsupportedClientInput
		}
		failure := newFailure(
			reason,
			request.exchangeID,
			0,
			err,
		)
		failure.ClientField = classifyClientRequestField(request.body, err)
		return result, failure
	}
	decoded, err = decoded.WithEffectiveModel(selection.effectiveModel)
	if err != nil {
		return result, newFailure(
			ReasonUnsupportedAccessPlan,
			request.exchangeID,
			0,
			err,
		)
	}
	encodedProvider, backendRequestReport, err :=
		protocolPath.Backend().EncodeRequest(decoded)
	result.Translation = result.Translation.Merge(backendRequestReport)
	if err != nil {
		return result, newFailure(
			ReasonProviderRequestInvalid,
			request.exchangeID,
			0,
			err,
		)
	}
	clientHello, _ := request.ClientHelloObservation()
	attemptID, err := pipeline.attemptIDs.NewAttemptID()
	if err != nil {
		return result, newFailure(
			ReasonProviderRequestInvalid,
			request.exchangeID,
			0,
			err,
		)
	}
	frozenRequest, err := providertransport.NewRequest(
		providertransport.RequestOptions{
			RequestID:      attemptID,
			TargetRef:      selection.targetRef,
			Target:         selection.target,
			AccessRevision: selection.revision,
			PlanHash:       selection.planHash,
			Action:         action,
			Method:         encodedProvider.Method(),
			RelativePath:   encodedProvider.RelativePath(),
			Headers:        encodedProvider.Headers(),
			Body:           encodedProvider.Body(),
			SecretRef:      selection.secretRef,
			AuthDriverRef:  selection.authDriverRef,
			TransportPlan:  selection.transportPlan,
			ClientHello:    clientHello,
		},
	)
	if err != nil {
		return result, newFailure(
			ReasonProviderRequestInvalid,
			request.exchangeID,
			0,
			err,
		)
	}

	ledger := &CommitLedger{}
	if decoded.Stream {
		err = pipeline.executeStream(
			operationContext,
			request,
			selection,
			protocolPath,
			decoded,
			frozenRequest,
			downstream,
			ledger,
			&result,
		)
	} else {
		err = pipeline.executeComplete(
			operationContext,
			request,
			selection,
			protocolPath,
			decoded,
			frozenRequest,
			downstream,
			ledger,
			&result,
		)
	}
	result.Ledger = ledger.Snapshot()
	if err != nil {
		switch {
		case ReasonOf(err) == ReasonToolDecisionRejected ||
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

func (pipeline *Pipeline) observeAttempt(
	request ClientRequest,
	result Result,
	resultErr error,
) {
	if pipeline == nil || pipeline.observer == nil {
		return
	}
	observation := AttemptObservation{
		ExchangeID: request.exchangeID,
		AccessID:   request.ingress.AccessID(),
		Outcome:    result.Outcome,
		ReasonCode: ReasonOf(resultErr),
		Transport:  result.Transport.Clone(),
	}
	var failure *Failure
	if errors.As(resultErr, &failure) {
		observation.ProviderStatus = failure.ProviderStatus
		observation.ProviderField = failure.ProviderField
		observation.ClientField = failure.ClientField
	}
	ctx, cancel := context.WithTimeout(
		context.WithoutCancel(pipeline.ownerContext),
		pipeline.observeLimit,
	)
	defer cancel()
	_ = pipeline.observer.Observe(ctx, observation)
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
) error {
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
	if err := pipeline.decideTools(ctx, request, selection, intents); err != nil {
		result.Outcome = AttemptAborted
		return err
	}
	clientBody, clientResponseReport, err :=
		protocolPath.Client().EncodeResponse(
			decoded,
			providerResponse,
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
	if err := downstream.Begin(ctx, ResponseModeJSON); err != nil {
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
	return nil
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
) error {
	if err := downstream.Begin(ctx, ResponseModeEventStream); err != nil {
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

	var retryDeadline time.Time
	for {
		if err := ledger.RecordUpstreamSend(int64(len(frozenRequest.Body()))); err != nil {
			return pipeline.abortStream(
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
					retryDeadline = time.Now().Add(pipeline.hold.MaxDuration)
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
			return pipeline.abortStream(
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
			return pipeline.abortStream(
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
					retryDeadline = time.Now().Add(pipeline.hold.MaxDuration)
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
			return pipeline.abortStream(
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
			return pipeline.abortStream(
				ctx,
				request.exchangeID,
				downstream,
				ledger,
				failure,
			)
		}

		stream, err := protocolPath.Streaming().NewStream(decoded)
		if err != nil {
			_ = response.Body.Close()
			return pipeline.abortStream(
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
			stream,
			response.Body,
			downstream,
			ledger,
			result,
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
				retryDeadline = time.Now().Add(pipeline.hold.MaxDuration)
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
		return pipeline.abortStream(
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
	stream protocolpath.Stream,
	body io.ReadCloser,
	downstream Downstream,
	ledger *CommitLedger,
	result *Result,
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
			if err := downstream.Keepalive(ctx); err != nil {
				return newFailure(
					ReasonDownstreamDisconnected,
					request.exchangeID,
					0,
					err,
				)
			}
			resetTimer(keepalive, pipeline.streamBudgets.KeepaliveInterval)
		case result, open := <-readResults:
			if !open {
				streamComplete = true
				continue
			}
			before := stream.SemanticProgress()
			if len(result.fragment) > 0 {
				safe, decodeErr := stream.Feed(ctx, result.fragment)
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

	terminal, err := stream.FinishDecoded(ctx)
	if err != nil {
		return err
	}
	result.Translation = result.Translation.Merge(
		terminal.TranslationReport(),
	)
	intents := terminal.ToolIntents()
	if err := pipeline.decideTools(ctx, request, selection, intents); err != nil {
		_ = terminal.Reject()
		return err
	}
	release, err := terminal.Approve()
	if err != nil {
		return err
	}
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
	return ledger.RecordTerminal()
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
	intents []protocolcore.ToolIntent,
) error {
	if len(intents) == 0 {
		return nil
	}
	decisionRequest, err := NewToolDecisionRequest(
		request.exchangeID,
		selection.accessID,
		selection.revision,
		selection.planHash,
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
		return newFailure(
			ReasonToolDecisionRejected,
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
	allowed, _ := ledger.CanTransportResend(request.replayClass, true)
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
	allowed, _ := ledger.CanTransportResend(request.replayClass, true)
	return allowed
}

func (pipeline *Pipeline) waitForRetry(
	ctx context.Context,
	exchangeID string,
	ordinal uint32,
	statusCode int,
	deadline time.Time,
) error {
	if !time.Now().Before(deadline) {
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
	if ctx.Err() != nil && !errors.Is(err, context.DeadlineExceeded) {
		return newFailure(ReasonExchangeCanceled, exchangeID, 0, err)
	}
	if errors.Is(context.Cause(ctx), ErrRuntimeStopping) {
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
