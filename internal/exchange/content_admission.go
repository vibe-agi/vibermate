package exchange

import (
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
	return &decoded, report, nil
}
