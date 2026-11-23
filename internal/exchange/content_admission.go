package exchange

import (
	"errors"

	"github.com/vibe-agi/vibermate/internal/protocolcore"
	"github.com/vibe-agi/vibermate/internal/protocolpath"
)

func (pipeline *Pipeline) decodeAdmitted(request ClientRequest, path *protocolpath.Path, body []byte) (protocolcore.Request, protocolcore.TranslationReport, error) {
	if request.admitted != nil {
		return *request.admitted, request.admittedReport, nil
	}
	return path.Client().DecodeRequest(body)
}

func (pipeline *Pipeline) admitClientInput(request ClientRequest, body []byte, selection frozenSelection) (*protocolcore.Request, protocolcore.TranslationReport, error) {
	path, err := pipeline.protocolPaths.Select(selection.codecPlan, request.operation.id)
	if err != nil {
		return nil, protocolcore.TranslationReport{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
	}
	// Refused here as well as after the credential's selection, so a route a
	// restored revision holds never leases an account it cannot use.
	if err := checkTranslatedTarget(selection, path); err != nil {
		return nil, protocolcore.TranslationReport{}, newFailure(ReasonEnvironmentPlanInvalid, request.exchangeID, 0, err)
	}
	decoded, report, err := path.Client().DecodeRequest(body)
	if err != nil {
		reason := ReasonInvalidExchangeRequest
		if protocolcore.ReasonOf(err) == protocolcore.ReasonUnsupportedClientInput {
			reason = ReasonUnsupportedClientInput
		}
		failure := newFailure(reason, request.exchangeID, 0, err)
		failure.ClientField = classifyClientRequestField(body, err)
		return nil, report, failure
	}
	decoded = mergeClientProtocolEvidence(decoded, request.ClientProtocolEvidence())
	if err = protocolcore.ValidateRequestWithin(decoded, pipeline.bodyAdmission.policy.Content.Semantic); err != nil {
		return nil, report, newFailure(ReasonInvalidExchangeRequest, request.exchangeID, 0, err)
	}
	if model, mapped := selection.mappedModel(decoded.RequestedModel); mapped {
		decoded, err = protocolcore.WithEffectiveModelWithin(decoded, model, pipeline.bodyAdmission.policy.Content.Semantic)
		if err != nil {
			return nil, report, newFailure(ReasonInvalidExchangeRequest, request.exchangeID, 0, err)
		}
	}
	if err := admitProviderRequest(request.exchangeID, path, decoded); err != nil {
		return nil, report, err
	}
	return &decoded, report, nil
}

// admitProviderRequest lets a composed path's provider side refuse, right
// after client decode, input it cannot carry, such as an attachment that is
// a provider file reference (design 8.1). Nothing is sent. The refusal is
// rendered as a native error of the client's dialect, so a client that
// recovers from it (Claude Code strips an attachment it was refused with a
// capability token) can do so; it carries the class too.
func admitProviderRequest(exchangeID string, path *protocolpath.Path, request protocolcore.Request) error {
	refusal := path.AdmitProviderRequest(request)
	if refusal == nil {
		return nil
	}
	class := *refusal
	failure := newFailure(ReasonUnsupportedClientInput, exchangeID, 0, protocolcore.NewFailure(
		protocolcore.ReasonUnsupportedClientInput, "$",
		errors.New("the provider side cannot carry this client input"),
	))
	if class.Validate() != nil {
		return failure
	}
	failure.Provider = &class
	if native, err := path.RenderProviderError(request, class); err == nil &&
		native.ForDialect(path.Client().Dialect()) != nil {
		failure.NativeError = native
	}
	return failure
}
