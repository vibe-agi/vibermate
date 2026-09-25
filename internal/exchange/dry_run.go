package exchange

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

// DryRunResult contains only frozen decision references and changed field names.
// It never carries a credential, request body, transformed value, or network result.
type DryRunResult struct {
	EvaluatedAt                   time.Time `json:"evaluatedAt"`
	EnvironmentID                 string    `json:"environmentId"`
	EnvironmentRevision           uint64    `json:"environmentRevision"`
	EnvironmentDigest             string    `json:"environmentDigest"`
	DestinationKind               string    `json:"destinationKind"`
	ProviderOrigin                string    `json:"providerOrigin"`
	RouteID                       string    `json:"routeId"`
	RouteRevision                 uint64    `json:"routeRevision"`
	AccountID                     string    `json:"accountId"`
	AccountRevision               uint64    `json:"accountRevision"`
	RequestedModel                string    `json:"requestedModel"`
	EffectiveModel                string    `json:"effectiveModel"`
	ModelMapped                   bool      `json:"modelMapped"`
	NetworkExitID                 string    `json:"networkExitId"`
	NetworkExitRevision           uint64    `json:"networkExitRevision"`
	ProviderMethod                string    `json:"providerMethod"`
	ProviderPath                  string    `json:"providerPath"`
	BodyChanged                   bool      `json:"bodyChanged"`
	ProtocolChangedTopLevelFields []string  `json:"protocolChangedTopLevelFields"`
	ChangedHeaderNames            []string  `json:"changedHeaderNames"`
	ChangedTopLevelFields         []string  `json:"changedTopLevelFields"`
	Unverified                    []string  `json:"unverified"`

	bodyDigest [sha256.Size]byte
}

// DryRun follows the frozen upstream decision path without acquiring a
// credential, recording evidence, or opening a network connection. It does not
// claim that DNS, TLS, quota, or provider response will succeed.
func (pipeline *Pipeline) DryRun(ctx context.Context, request ClientRequest) (DryRunResult, error) {
	if pipeline == nil || pipeline.protocolPaths == nil || pipeline.now == nil || ctx == nil {
		return DryRunResult{}, errors.New("Exchange dry run is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return DryRunResult{}, err
	}
	if err := request.validate(); err != nil {
		return DryRunResult{}, newFailure(ReasonInvalidExchangeRequest, request.exchangeID, 0, err)
	}
	selection, err := selectFrozenPlan(request.plan)
	if err != nil {
		return DryRunResult{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
	}
	if err := validateClientOperation(request.plan, request.operation, request.replayClass); err != nil {
		return DryRunResult{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
	}
	logicalBody, err := logicalClientRequestBody(request)
	if err != nil && !selection.original {
		return DryRunResult{}, newFailure(ReasonInvalidExchangeRequest, request.exchangeID, 0, err)
	}
	startedAt := pipeline.now().UTC()
	if selection.original {
		return pipeline.dryRunOriginal(ctx, request, selection, logicalBody, startedAt)
	}
	candidate, err := pipeline.selectCredentialCandidate(ctx, request, logicalBody, selection, startedAt)
	if err != nil {
		return DryRunResult{}, err
	}
	if candidate.mode != providerauth.CredentialManaged {
		return DryRunResult{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0,
			errors.New("dry run requires a managed upstream account"))
	}
	path, err := pipeline.protocolPaths.Select(selection.codecPlan, request.operation.id)
	if err != nil {
		return DryRunResult{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
	}
	decoded, _, err := path.Client().DecodeRequest(logicalBody)
	if err != nil {
		reason := ReasonInvalidExchangeRequest
		if protocolcore.ReasonOf(err) == protocolcore.ReasonUnsupportedClientInput {
			reason = ReasonUnsupportedClientInput
		}
		return DryRunResult{}, newFailure(reason, request.exchangeID, 0, err)
	}
	requestedModel := decoded.RequestedModel
	mappedModel, mapped := selection.mappedModel(requestedModel)
	if mapped {
		decoded, err = decoded.WithEffectiveModel(mappedModel)
		if err != nil {
			return DryRunResult{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
		}
	}
	decoded = mergeClientProtocolEvidence(decoded, request.ClientProtocolEvidence())
	providerRequest, _, err := path.EncodeProviderRequest(decoded, logicalBody, request.protocolHeaders())
	if err != nil {
		return DryRunResult{}, newFailure(ReasonProviderRequestInvalid, request.exchangeID, 0, err)
	}
	headers := providerRequest.Headers()
	for name, values := range nativeChatGPTProtocolHeaders(request, selection) {
		headers[name] = values
	}
	refreshChatGPTRoutingHint(headers, logicalBody, providerRequest.Body())
	providerPath := upstreamendpoint.ProviderRelativePath(selection.target.Origin(), providerRequest.RelativePath())
	turn, err := newMessageTransformTurn(request, pipeline.annotations, startedAt)
	if err != nil {
		return DryRunResult{}, newFailure(ReasonMessageTransformFailed, request.exchangeID, 0, err)
	}
	transformedHeaders, transformedBody, _, err := applyRequestMessageTransform(
		ctx, turn, providerRequest.Method(), "/"+strings.TrimPrefix(providerPath, "/"),
		headers, providerRequest.Body(),
	)
	if err != nil {
		return DryRunResult{}, newFailure(ReasonMessageTransformFailed, request.exchangeID, 0, err)
	}
	if selection.codecPlan.ProviderDialect() == protocolspec.DialectOpenAIResponses &&
		upstreamendpoint.IsChatGPTCodexOrigin(selection.target.Origin()) {
		refreshChatGPTRoutingHint(transformedHeaders, providerRequest.Body(), transformedBody)
	}
	profile := request.plan.EgressProfile()
	unverified := []string{"account_link", "credential", "dns", "tls", "quota", "provider_response", "runtime_time"}
	if _, hasAdmission := request.CaptureAdmission(); !hasAdmission {
		unverified = append(unverified, "runtime_identity")
	}
	return DryRunResult{
		EvaluatedAt:   startedAt,
		EnvironmentID: selection.environmentID.String(), EnvironmentRevision: uint64(selection.environmentRevision),
		EnvironmentDigest: selection.environmentDigest.String(),
		DestinationKind:   "upstream", ProviderOrigin: selection.target.Origin().String(),
		RouteID: selection.routeID.String(), RouteRevision: uint64(selection.routeRevision),
		AccountID: candidate.account.ID, AccountRevision: uint64(candidate.account.Revision),
		RequestedModel: requestedModel, EffectiveModel: decoded.EffectiveModel, ModelMapped: mapped,
		NetworkExitID: profile.ID.String(), NetworkExitRevision: uint64(profile.Revision),
		ProviderMethod: providerRequest.Method(), ProviderPath: "/" + strings.TrimPrefix(providerPath, "/"),
		BodyChanged:                   !bytes.Equal(providerRequest.Body(), transformedBody),
		ProtocolChangedTopLevelFields: changedTopLevelFields(logicalBody, providerRequest.Body()),
		ChangedHeaderNames:            changedHeaderNames(headers, transformedHeaders),
		ChangedTopLevelFields:         changedTopLevelFields(providerRequest.Body(), transformedBody),
		Unverified:                    unverified,
		bodyDigest:                    sha256.Sum256(transformedBody),
	}, nil
}

func (pipeline *Pipeline) dryRunOriginal(
	ctx context.Context,
	request ClientRequest,
	selection frozenSelection,
	logicalBody []byte,
	startedAt time.Time,
) (DryRunResult, error) {
	headers, available := request.OriginalHeaders()
	if !available {
		return DryRunResult{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0,
			errors.New("Original Destination request headers are unavailable"))
	}
	turn, err := newMessageTransformTurn(request, pipeline.annotations, startedAt)
	if err != nil {
		return DryRunResult{}, newFailure(ReasonMessageTransformFailed, request.exchangeID, 0, err)
	}
	transformedHeaders, transformedBody, _, err := applyRequestMessageTransform(
		ctx, turn, request.operation.Method(), request.operation.Path(), headers, request.body,
	)
	if err != nil {
		return DryRunResult{}, newFailure(ReasonMessageTransformFailed, request.exchangeID, 0, err)
	}
	requestedModel := ""
	unverified := []string{"client_authentication", "client_headers", "dns", "tls", "quota", "provider_response", "runtime_identity", "runtime_time"}
	if path, selectErr := pipeline.protocolPaths.Select(selection.codecPlan, request.operation.id); selectErr == nil && logicalBody != nil {
		if decoded, _, decodeErr := path.Client().DecodeRequest(logicalBody); decodeErr == nil {
			requestedModel = decoded.RequestedModel
		}
	}
	if requestedModel == "" {
		unverified = append(unverified, "semantic_decode")
	}
	if request.operation.RawQuery() != "" {
		unverified = append(unverified, "raw_query")
	}
	profile := request.plan.EgressProfile()
	return DryRunResult{
		EvaluatedAt:   startedAt,
		EnvironmentID: selection.environmentID.String(), EnvironmentRevision: uint64(selection.environmentRevision),
		EnvironmentDigest: selection.environmentDigest.String(),
		DestinationKind:   "original", ProviderOrigin: selection.target.Origin().String(),
		RequestedModel: requestedModel, EffectiveModel: requestedModel,
		NetworkExitID: profile.ID.String(), NetworkExitRevision: uint64(profile.Revision),
		ProviderMethod: request.operation.Method(), ProviderPath: request.operation.Path(),
		BodyChanged:                   !bytes.Equal(request.body, transformedBody),
		ProtocolChangedTopLevelFields: []string{},
		ChangedHeaderNames:            changedHeaderNames(headers, transformedHeaders),
		ChangedTopLevelFields:         changedTopLevelFields(request.body, transformedBody),
		Unverified:                    unverified,
		bodyDigest:                    sha256.Sum256(transformedBody),
	}, nil
}

func changedHeaderNames(before, after http.Header) []string {
	names := make(map[string]struct{}, len(before)+len(after))
	for name := range before {
		names[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for name := range after {
		names[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	changed := make([]string, 0, len(names))
	for name := range names {
		if !equalHeaderValues(before.Values(name), after.Values(name)) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

func equalHeaderValues(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func changedTopLevelFields(before, after []byte) []string {
	left, leftOK := decodeDryRunObject(before)
	right, rightOK := decodeDryRunObject(after)
	if !leftOK || !rightOK {
		return nil
	}
	fields := make(map[string]struct{}, len(left)+len(right))
	for field := range left {
		fields[field] = struct{}{}
	}
	for field := range right {
		fields[field] = struct{}{}
	}
	changed := make([]string, 0, len(fields))
	for field := range fields {
		leftValue, leftExists := left[field]
		rightValue, rightExists := right[field]
		if leftExists != rightExists || !reflect.DeepEqual(leftValue, rightValue) {
			if !safeDryRunField(field) {
				field = "[redacted]"
			}
			changed = append(changed, field)
		}
	}
	sort.Strings(changed)
	if len(changed) > 64 {
		changed = append(changed[:64], "[more fields omitted]")
	}
	return changed
}

func decodeDryRunObject(body []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || value == nil {
		return nil, false
	}
	var trailing any
	return value, decoder.Decode(&trailing) == io.EOF
}

func safeDryRunField(field string) bool {
	if field == "" || len(field) > 64 || strings.ContainsAny(field, " ./\\:\r\n\t") {
		return false
	}
	lower := strings.ToLower(field)
	for _, protected := range []string{"token", "secret", "password", "cookie", "authorization", "api_key", "credential"} {
		if strings.Contains(lower, protected) {
			return false
		}
	}
	for _, character := range field {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}
