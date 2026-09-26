package providertransport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/vibe-agi/vibermate/internal/codexoauth"
	"github.com/vibe-agi/vibermate/internal/egressaudit"
	"github.com/vibe-agi/vibermate/internal/offlinehold"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/secretstore"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
	"github.com/vibe-agi/vibermate/internal/wireprofile"
)

const modelsDevOrigin = "https://models.dev"

// InstanceIDSource mints opaque, independent identities. Runtime-owned
// requests need three identities because the logical action, queued egress,
// and immutable audit attempt are different authorities and lifetimes.
type InstanceIDSource interface {
	NewInstanceID(context.Context) (string, error)
}

type runtimeFetchSpec struct {
	purpose      egressaudit.EgressPurpose
	targetRef    string
	target       Target
	relativePath string
	credential   providerauth.Lease
	method       string
	body         []byte
	contentType  string
	captured     *capturedAccountRead
}

// clearingRequestBody gives the transport its own copy of a Runtime-owned
// request body and erases that copy when the RoundTripper releases it. A
// transport may close a request body after RoundTrip returns, so the caller's
// stack defer is not a safe lifetime boundary for refresh-token bytes.
type clearingRequestBody struct {
	reader *bytes.Reader
	value  []byte
	once   sync.Once
}

func newClearingRequestBody(value []byte) *clearingRequestBody {
	cloned := bytes.Clone(value)
	return &clearingRequestBody{reader: bytes.NewReader(cloned), value: cloned}
}

func (body *clearingRequestBody) Read(destination []byte) (int, error) {
	return body.reader.Read(destination)
}

func (body *clearingRequestBody) Close() error {
	body.once.Do(func() {
		clear(body.value)
		body.value = nil
	})
	return nil
}

var _ io.ReadCloser = (*clearingRequestBody)(nil)

// FetchEndpointModels discovers the models that this exact Endpoint says are
// available. Metadata catalogs are deliberately not consulted here: they can
// describe a provider family but cannot prove availability at an Endpoint.
func (client *Client) FetchEndpointModels(
	ctx context.Context,
	endpoint upstreamendpoint.Endpoint,
	credential providerauth.Lease,
) (*http.Response, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, fmt.Errorf("discover Endpoint models: %w", err)
	}
	if endpoint.State != upstreamendpoint.StateActive {
		return nil, upstreamendpoint.ErrEndpointDisabled
	}
	if credential == nil || credential.Mode() != providerauth.CredentialManaged {
		return nil, errors.New("discover Endpoint models: managed credential is required")
	}
	account, hasAccount := credential.Account()
	if !hasAccount || account.Validate() != nil {
		return nil, errors.New("discover Endpoint models: account evidence is invalid")
	}
	target, err := NewTarget(endpoint.Origin)
	if err != nil {
		return nil, fmt.Errorf("discover Endpoint models: %w", err)
	}
	return client.fetchRuntimeJSON(ctx, runtimeFetchSpec{
		purpose:      egressaudit.PurposeUpstreamModelDiscovery,
		targetRef:    endpoint.ID.String(),
		target:       target,
		relativePath: upstreamendpoint.ModelsPath(endpoint.Origin),
		credential:   credential,
	})
}

// FetchModelsDev reads the fixed metadata directory. It is an enrichment
// source only and travels under the runtime auxiliary-egress purpose.
func (client *Client) FetchModelsDev(ctx context.Context) (*http.Response, error) {
	origin, err := originidentity.ParseProviderOrigin(modelsDevOrigin)
	if err != nil {
		return nil, fmt.Errorf("construct models.dev origin: %w", err)
	}
	target, err := NewTarget(origin)
	if err != nil {
		return nil, fmt.Errorf("construct models.dev target: %w", err)
	}
	return client.fetchRuntimeJSON(ctx, runtimeFetchSpec{
		purpose:      egressaudit.PurposeModelMetadataDirectory,
		targetRef:    "runtime.models-dev",
		target:       target,
		relativePath: "/models.json",
	})
}

// DoCodexOAuthTokenRequest sends only the fixed Codex OAuth token exchange
// through the runtime's mandatory Hold, strict transport, and body-free audit
// boundary. The refresh token is never emitted as Raw evidence.
func (client *Client) DoCodexOAuthTokenRequest(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.Body == nil ||
		request.Method != http.MethodPost || request.URL.String() != codexoauth.TokenURL ||
		(request.Header.Get("Content-Type") != "application/json" &&
			request.Header.Get("Content-Type") != "application/x-www-form-urlencoded") {
		return nil, errors.New("Codex OAuth token request is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 64<<10+1))
	closeErr := request.Body.Close()
	if err != nil || closeErr != nil || len(body) == 0 || len(body) > 64<<10 {
		clear(body)
		return nil, errors.New("Codex OAuth token request body is invalid")
	}
	defer clear(body)
	origin, err := originidentity.ParseProviderOrigin("https://auth.openai.com")
	if err != nil {
		return nil, fmt.Errorf("construct Codex OAuth origin: %w", err)
	}
	target, err := NewTarget(origin)
	if err != nil {
		return nil, fmt.Errorf("construct Codex OAuth target: %w", err)
	}
	return client.fetchRuntimeJSON(request.Context(), runtimeFetchSpec{
		purpose:      egressaudit.PurposeCredentialRefresh,
		targetRef:    "runtime.codex-oauth",
		target:       target,
		relativePath: "/oauth/token",
		method:       http.MethodPost,
		body:         body,
		contentType:  request.Header.Get("Content-Type"),
	})
}

func (client *Client) fetchRuntimeJSON(
	ctx context.Context,
	spec runtimeFetchSpec,
) (*http.Response, error) {
	if ctx == nil {
		return nil, errors.New("runtime fetch context is nil")
	}
	if client == nil || client.instanceIDs == nil {
		return nil, errors.New("runtime fetch identity source is unavailable")
	}
	if err := spec.target.validate(); err != nil {
		return nil, err
	}
	if spec.method == "" {
		spec.method = http.MethodGet
	}
	actionID, requestID, attemptID, err := client.runtimeFetchIDs(ctx)
	if err != nil {
		return nil, err
	}
	operationContext, operation, err := client.begin(ctx)
	if err != nil {
		return nil, err
	}
	handoff := false
	var action *offlinehold.ActionLease
	defer func() {
		if handoff {
			return
		}
		client.finish(operation, nil)
		if action != nil {
			action.Release()
		}
	}()
	action, err = client.coordinator.BeginAction(
		operationContext,
		offlinehold.ActionRequest{ActionID: actionID},
	)
	if err != nil {
		return nil, fmt.Errorf("begin runtime egress action: %w", err)
	}
	probeTarget, err := client.runtimeProbeTarget(spec)
	if err != nil {
		return nil, err
	}
	lease, err := client.coordinator.Acquire(
		operationContext,
		offlinehold.AcquireRequest{
			RequestID: requestID,
			Action:    action,
			Target:    probeTarget,
			SizeBytes: int64(len(spec.body)),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("acquire runtime egress lease: %w", err)
	}
	operation.setLease(lease)
	if err := operationContext.Err(); err != nil {
		return nil, err
	}

	requestURL := url.URL{
		Scheme: spec.target.Origin().Scheme(),
		Host:   spec.target.HTTPAuthority(),
		Path:   spec.relativePath,
	}
	var requestBody io.Reader = bytes.NewReader(nil)
	var sensitiveBody *clearingRequestBody
	if len(spec.body) != 0 {
		sensitiveBody = newClearingRequestBody(spec.body)
		requestBody = sensitiveBody
	}
	transportOwnsBody := false
	defer func() {
		if sensitiveBody != nil && !transportOwnsBody {
			_ = sensitiveBody.Close()
		}
	}()
	request, err := http.NewRequestWithContext(
		operationContext,
		spec.method,
		requestURL.String(),
		requestBody,
	)
	if err != nil {
		return nil, err
	}
	request.Host = spec.target.HTTPAuthority()
	request.Header.Set("Accept", "application/json")
	if spec.contentType != "" {
		request.Header.Set("Content-Type", spec.contentType)
	}
	request.ContentLength = int64(len(spec.body))
	if spec.credential != nil {
		account, hasAccount := spec.credential.Account()
		authenticator, supported := client.authenticators[spec.credential.Driver()]
		if !hasAccount || account.Validate() != nil || !supported {
			return nil, errors.New("runtime fetch credential authority is invalid")
		}
		if _, err := authenticator.Apply(
			operationContext,
			request,
			spec.credential.Secret(),
			secretstore.Revision(account.CredentialEpoch),
			spec.target,
		); err != nil {
			return nil, fmt.Errorf("finalize runtime fetch authentication: %w", err)
		}
	}
	if spec.purpose == egressaudit.PurposeUpstreamModelDiscovery {
		request.URL.RawQuery = upstreamendpoint.ModelsQuery(spec.target.Origin(), request.Header)
	}

	attempt, err := client.beginRuntimeAudit(
		operationContext,
		attemptID,
		actionID,
		spec,
	)
	if err != nil {
		return nil, err
	}
	transportOwnsBody = true
	dispatch := TransportDispatch{target: spec.target, plan: client.runtimePlan}
	if spec.captured != nil {
		dispatch.egressPolicy = spec.captured.plan.EgressPolicy()
	}
	response, transportEvidence, err := client.transport.RoundTrip(request, dispatch)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		client.completeAudit(
			operationContext,
			attempt,
			egressaudit.OutcomeFailed,
			transportFailureClass(err, transportEvidence.FallbackReason()),
			0,
			0,
		)
		return nil, fmt.Errorf("send runtime request: %w", err)
	}
	if response == nil || response.Body == nil {
		client.completeAudit(
			operationContext,
			attempt,
			egressaudit.OutcomeFailed,
			"incomplete_response",
			int64(len(spec.body)),
			0,
		)
		return nil, errors.New("runtime transport returned an incomplete response")
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		closeErr := response.Body.Close()
		client.completeAudit(
			operationContext,
			attempt,
			egressaudit.OutcomeFailed,
			"redirect_denied",
			0,
			0,
		)
		return nil, errors.Join(ErrRedirectNotAllowed, closeErr)
	}

	responseBody := response.Body
	counted := &countingReader{reader: responseBody}
	body := &leaseBody{
		reader: counted,
		close:  responseBody,
		finish: func(terminalErr error) {
			outcome, errorClass := responseAuditTerminal(
				operationContext,
				terminalErr,
			)
			client.completeAudit(
				context.WithoutCancel(operationContext),
				attempt,
				outcome,
				errorClass,
				int64(len(spec.body)),
				counted.count(),
			)
			client.finish(operation, terminalErr)
			action.Release()
		},
	}
	operation.setBody(body)
	response.Body = body
	handoff = true
	return response, nil
}

func (client *Client) runtimeFetchIDs(
	ctx context.Context,
) (string, string, string, error) {
	values := make([]string, 3)
	for index := range values {
		value, err := client.instanceIDs.NewInstanceID(ctx)
		if err != nil {
			return "", "", "", fmt.Errorf(
				"mint runtime egress identity: %w",
				err,
			)
		}
		values[index] = value
	}
	return values[0], values[1], values[2], nil
}

func (client *Client) runtimeProbeTarget(
	spec runtimeFetchSpec,
) (offlinehold.ProbeTarget, error) {
	kind, err := offlinehold.KindForPurpose(spec.purpose)
	if err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	transport, err := spec.target.probeTransportKind()
	if err != nil {
		return offlinehold.ProbeTarget{}, err
	}
	requested := client.runtimePlan.Requested()
	digest := sha256.Sum256([]byte(strings.Join([]string{
		string(spec.purpose),
		spec.targetRef,
		spec.target.Origin().String(),
		spec.relativePath,
		spec.method,
		requested.Ref().String(),
		fmt.Sprintf("%d", requested.Revision()),
	}, "\x00")))
	target := offlinehold.ProbeTarget{
		Kind:          kind,
		Transport:     transport,
		TargetRef:     spec.targetRef,
		NetworkOrigin: spec.target.Origin().String(),
		HTTPAuthority: spec.target.HTTPAuthority(),
		TLSServerName: spec.target.TLSServerName(),
		PlanRevision:  uint64(requested.Revision()),
		PlanDigest:    hex.EncodeToString(digest[:]),
	}
	if spec.captured != nil {
		target.EgressPolicy = spec.captured.plan.EgressPolicy()
		target.PlanDigest = spec.captured.plan.EnvironmentDigest().String()
		target.PlanRevision = uint64(spec.captured.plan.EnvironmentRevision())
	}
	if err := target.Validate(); err != nil {
		return offlinehold.ProbeTarget{}, fmt.Errorf(
			"construct runtime probe target: %w",
			err,
		)
	}
	return target, nil
}

func (client *Client) beginRuntimeAudit(
	ctx context.Context,
	attemptID string,
	actionID string,
	spec runtimeFetchSpec,
) (egressaudit.Attempt, error) {
	if client.audit == nil {
		return egressaudit.Attempt{}, nil
	}
	authority, err := egressaudit.AuthorityForPurpose(spec.purpose)
	if err != nil {
		return egressaudit.Attempt{}, err
	}
	input := egressaudit.NewInput{
		ID:           attemptID,
		Purpose:      spec.purpose,
		PayloadClass: egressaudit.PayloadRuntime,
		Parent: egressaudit.ParentRef{
			Kind: egressaudit.ParentRuntimeAction,
			ID:   actionID,
		},
		Caller:       egressaudit.CallerCore,
		TargetOrigin: spec.target.Origin().String(),
		Decision:     egressaudit.BuiltInDirectDecision(authority),
		StartedAt:    client.clock(),
	}
	if spec.captured != nil {
		plan := spec.captured.plan
		route, _, err := plan.AccountReadTarget()
		if err != nil {
			return egressaudit.Attempt{}, err
		}
		input.ConnectionID = spec.captured.connectionID
		input.PayloadClass = egressaudit.PayloadControl
		input.Parent = egressaudit.ParentRef{Kind: egressaudit.ParentClientOperation, ID: spec.captured.operationID}
		input.Decision = egressaudit.DecisionRef{PolicyID: string(plan.EnvironmentID()), PolicyRevision: uint64(plan.EnvironmentRevision()),
			Authority: egressaudit.AuthorityEnvironment, RuleID: string(route.ID()), ProxyID: string(plan.EgressProfile().ID)}
	}
	attempt, err := egressaudit.New(input)
	if err != nil {
		return egressaudit.Attempt{}, fmt.Errorf(
			"construct runtime EgressAttempt: %w",
			err,
		)
	}
	if _, err := client.audit.Append(ctx, attempt); err != nil {
		return egressaudit.Attempt{}, fmt.Errorf(
			"record runtime EgressAttempt: %w",
			err,
		)
	}
	return attempt, nil
}

func standardRuntimeTransportPlan() (
	wireprofile.CompiledTransportFingerprintPlan,
	error,
) {
	catalog, err := wireprofile.BuiltInCatalog()
	if err != nil {
		return wireprofile.CompiledTransportFingerprintPlan{}, err
	}
	ref, err := wireprofile.NewTransportProfileRef(
		wireprofile.TransportProfileStandardH1Value,
	)
	if err != nil {
		return wireprofile.CompiledTransportFingerprintPlan{}, err
	}
	return catalog.ResolveTransport(ref)
}

var _ io.ReadCloser = (*leaseBody)(nil)
