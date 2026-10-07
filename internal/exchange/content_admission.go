package exchange

import (
	"context"
	"errors"

	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/exchangecontent"
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
	if err = pipeline.preflightContent(request, decoded); err != nil {
		return nil, report, newFailure(ReasonInvalidExchangeRequest, request.exchangeID, 0, err)
	}
	return &decoded, report, nil
}

func (pipeline *Pipeline) preflightContent(request ClientRequest, decoded protocolcore.Request) error {
	if request.plan.ContentRecording().Mode == environment.ContentRecordingOff {
		return nil
	}
	plan := request.plan
	endpoint, protocolPlan := plan.Endpoint(), plan.ProtocolPlan()
	routeID, routeRevision := requestPlanRouteRef(plan)
	source, err := exchangecontent.NewSourceWithin(pipeline.bodyAdmission.policy.Content, request.exchangeID, exchangecontent.FrozenRef{
		EnvironmentID: plan.EnvironmentID().String(), EnvironmentRevision: uint64(plan.EnvironmentRevision()), EnvironmentDigest: plan.EnvironmentDigest().String(),
		ClientEndpointID: endpoint.ID().String(), ClientEndpointRevision: uint64(endpoint.Revision()), ProtocolPlanID: protocolPlan.ID().String(), ProtocolPlanRevision: uint64(protocolPlan.Revision()), RouteID: routeID.String(), RouteRevision: uint64(routeRevision),
	}, plan.ContentRecording(), pipeline.now(), decoded, nil, exchangecontent.WithParentRef(exchangecontent.ParentRef{CaptureRunID: request.CaptureRunRef(), ManualCaptureID: request.ManualCaptureRef()}))
	if err != nil {
		return err
	}
	cost, err := source.Measure(context.Background())
	if err != nil {
		return err
	}
	reserve, _, err := RequiredResponseReservation(pipeline.bodyAdmission.policy.Content.Semantic)
	if err != nil {
		return err
	}
	l := pipeline.bodyAdmission.policy.Content
	if cost.TranscriptNodes >= 100001 || cost.MaxPhysicalSlots > 16384 || cost.MaxAgentBytes > 4096 {
		return errors.New("request cannot fit Source physical representation with response node")
	}
	if cost.RetainedBytes > l.RetainedBytes-reserve.RetainedBytes || cost.CanonicalBytes > l.CanonicalBytes-reserve.CanonicalBytes || cost.StructureBytes > l.StructureBytes-reserve.StructureBytes {
		return errors.New("request consumes the fixed response record reservation")
	}
	return nil
}
