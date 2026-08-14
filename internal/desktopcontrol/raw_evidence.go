package desktopcontrol

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/vibe-agi/vibermate/internal/rawevidence"
)

type rawEvidenceEnvelopeView struct {
	EnvelopeID               string                   `json:"envelopeId"`
	Layer                    rawevidence.Layer        `json:"layer"`
	ScopeKind                rawevidence.ScopeKind    `json:"scopeKind"`
	ScopeID                  string                   `json:"scopeId,omitempty"`
	ExchangeID               string                   `json:"exchangeId"`
	ConnectionID             string                   `json:"connectionId,omitempty"`
	AttemptID                string                   `json:"attemptId,omitempty"`
	EnvironmentID            string                   `json:"environmentId,omitempty"`
	EnvironmentRevision      uint64                   `json:"environmentRevision,omitempty"`
	EnvironmentDigest        string                   `json:"environmentDigest,omitempty"`
	ClientEndpointID         string                   `json:"clientEndpointId,omitempty"`
	ClientEndpointRevision   uint64                   `json:"clientEndpointRevision,omitempty"`
	UpstreamEndpointID       string                   `json:"upstreamEndpointId,omitempty"`
	UpstreamEndpointRevision uint64                   `json:"upstreamEndpointRevision,omitempty"`
	ProtocolPlanID           string                   `json:"protocolPlanId,omitempty"`
	ProtocolPlanRevision     uint64                   `json:"protocolPlanRevision,omitempty"`
	RouteID                  string                   `json:"routeId,omitempty"`
	RouteRevision            uint64                   `json:"routeRevision,omitempty"`
	AccountID                string                   `json:"accountId,omitempty"`
	AccountRevision          uint64                   `json:"accountRevision,omitempty"`
	CredentialEpoch          uint64                   `json:"credentialEpoch,omitempty"`
	ObservedAt               time.Time                `json:"observedAt"`
	ExpiresAt                time.Time                `json:"expiresAt"`
	Method                   string                   `json:"method,omitempty"`
	StatusCode               int                      `json:"statusCode,omitempty"`
	Scheme                   string                   `json:"scheme,omitempty"`
	Authority                string                   `json:"authority,omitempty"`
	Path                     string                   `json:"path,omitempty"`
	RawQuery                 string                   `json:"rawQuery,omitempty"`
	ContentType              string                   `json:"contentType,omitempty"`
	ContentEncoding          string                   `json:"contentEncoding,omitempty"`
	Representation           string                   `json:"representation,omitempty"`
	Canonicalization         string                   `json:"canonicalization,omitempty"`
	HeaderCount              int                      `json:"headerCount"`
	TrailerCount             int                      `json:"trailerCount"`
	BodyBytes                int64                    `json:"bodyBytes"`
	BodySHA256               string                   `json:"bodySha256,omitempty"`
	DigestScope              rawevidence.DigestScope  `json:"digestScope"`
	PayloadState             rawevidence.PayloadState `json:"payloadState"`
	PayloadReason            string                   `json:"payloadReason,omitempty"`
	ContainsSecret           bool                     `json:"containsSecret"`
	RevealAvailable          bool                     `json:"revealAvailable"`
}

type rawEvidenceListResponse struct {
	Items    []rawEvidenceEnvelopeView `json:"items"`
	Recovery rawEvidenceRecoveryView   `json:"recovery"`
	Writer   rawEvidenceWriterView     `json:"writer"`
}

type rawEvidenceRecoveryView struct {
	RecoveredUncleanWriters uint64 `json:"recoveredUncleanWriters"`
	PurgedExpiredEnvelopes  uint64 `json:"purgedExpiredEnvelopes"`
	MaximumPossibleLossMS   int64  `json:"maximumPossibleLossMs"`
}

type rawEvidenceWriterView struct {
	State                  string `json:"state"`
	AdmittedRecords        uint64 `json:"admittedRecords"`
	DurableWatermark       uint64 `json:"durableWatermark"`
	QueueRecords           int    `json:"queueRecords"`
	QueueBytes             int64  `json:"queueBytes"`
	LastFailure            string `json:"lastFailure,omitempty"`
	MaximumUnflushedTimeMS int64  `json:"maximumUnflushedTimeMs"`
}

type rawEvidenceStatisticsReader interface {
	Statistics() rawevidence.Statistics
}

type rawRevealResponse struct {
	Envelope   rawEvidenceEnvelopeView   `json:"envelope"`
	Headers    []rawevidence.HeaderField `json:"headers"`
	Trailers   []rawevidence.HeaderField `json:"trailers"`
	BodyBase64 string                    `json:"bodyBase64"`
	Frames     []rawevidence.Frame       `json:"frames"`
}

func (handler *Handler) listRawEvidence(
	writer http.ResponseWriter,
	request *http.Request,
) {
	exchangeID := request.PathValue("exchangeId")
	items, err := handler.rawEvidence.ListExchange(request.Context(), exchangeID)
	if errors.Is(err, rawevidence.ErrInvalidRead) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRawEvidenceUnavailable)
		return
	}
	views := make([]rawEvidenceEnvelopeView, len(items))
	for index := range items {
		views[index] = rawEvidenceViewOf(items[index])
	}
	recovery := handler.rawEvidence.Recovery()
	writeJSON(writer, http.StatusOK, rawEvidenceListResponse{
		Items: views,
		Recovery: rawEvidenceRecoveryView{
			RecoveredUncleanWriters: recovery.RecoveredUncleanWriters,
			PurgedExpiredEnvelopes:  recovery.PurgedExpiredEnvelopes,
			MaximumPossibleLossMS:   recovery.MaximumPossibleLoss.Milliseconds(),
		},
		Writer: rawEvidenceWriterViewOf(handler.rawEvidence),
	})
}

func rawEvidenceWriterViewOf(reader rawevidence.Reader) rawEvidenceWriterView {
	statisticsReader, ok := reader.(rawEvidenceStatisticsReader)
	if !ok {
		return rawEvidenceWriterView{State: "unavailable"}
	}
	statistics := statisticsReader.Statistics()
	state := "active"
	if statistics.LastFailure != "" {
		state = "degraded"
	}
	return rawEvidenceWriterView{
		State:                  state,
		AdmittedRecords:        statistics.AdmittedRecords,
		DurableWatermark:       statistics.DurableWatermark,
		QueueRecords:           statistics.QueueRecords,
		QueueBytes:             statistics.QueueBytes,
		LastFailure:            statistics.LastFailure,
		MaximumUnflushedTimeMS: statistics.MaximumUnflushedTime.Milliseconds(),
	}
}

func (handler *Handler) revealRawEvidence(
	writer http.ResponseWriter,
	request *http.Request,
) {
	if request.Body != nil {
		body, err := io.ReadAll(io.LimitReader(request.Body, 2))
		if err != nil || len(body) != 0 {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
	}
	revealRequest := rawevidence.RevealRequest{
		EnvelopeID: request.PathValue("envelopeId"),
		ActorID:    "desktop-app:" + handler.status.Status().InstanceID,
	}
	if err := revealRequest.Validate(); err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	revealed, err := handler.rawEvidence.Reveal(request.Context(), revealRequest)
	switch {
	case errors.Is(err, rawevidence.ErrEnvelopeNotFound):
		writeProblem(writer, http.StatusNotFound, ReasonRawEvidenceNotFound)
		return
	case errors.Is(err, rawevidence.ErrPayloadUnavailable):
		writeProblem(writer, http.StatusGone, ReasonRawEvidenceUnavailable)
		return
	case err != nil:
		writeProblem(writer, http.StatusServiceUnavailable, ReasonRawEvidenceUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, rawRevealResponse{
		Envelope:   rawEvidenceViewOf(revealed.Metadata),
		Headers:    rawHeaderFields(revealed.Payload.Headers),
		Trailers:   rawHeaderFields(revealed.Payload.Trailers),
		BodyBase64: base64.StdEncoding.EncodeToString(revealed.Payload.Body),
		Frames:     rawFrames(revealed.Payload.Frames),
	})
}

// Raw evidence collections are required JSON arrays on the public control
// contract. Go's nil slices otherwise encode as null, even though nil and an
// empty slice have the same in-process meaning.
func rawHeaderFields(values []rawevidence.HeaderField) []rawevidence.HeaderField {
	if values == nil {
		return []rawevidence.HeaderField{}
	}
	return values
}

func rawFrames(values []rawevidence.Frame) []rawevidence.Frame {
	if values == nil {
		return []rawevidence.Frame{}
	}
	return values
}

func rawEvidenceViewOf(value rawevidence.EnvelopeMetadata) rawEvidenceEnvelopeView {
	digest := ""
	if value.DigestScope != rawevidence.DigestUnavailable {
		digest = hex.EncodeToString(value.BodySHA256[:])
	}
	return rawEvidenceEnvelopeView{
		EnvelopeID: value.EnvelopeID, Layer: value.Layer,
		ScopeKind: value.ScopeKind, ScopeID: value.ScopeID,
		ExchangeID: value.ExchangeID, ConnectionID: value.ConnectionID,
		AttemptID: value.AttemptID, EnvironmentID: value.EnvironmentID,
		EnvironmentRevision:      value.EnvironmentRevision,
		EnvironmentDigest:        value.EnvironmentDigest,
		ClientEndpointID:         value.ClientEndpointID,
		ClientEndpointRevision:   value.ClientEndpointRevision,
		UpstreamEndpointID:       value.UpstreamEndpointID,
		UpstreamEndpointRevision: value.UpstreamEndpointRevision,
		ProtocolPlanID:           value.ProtocolPlanID,
		ProtocolPlanRevision:     value.ProtocolPlanRevision,
		RouteID:                  value.RouteID, RouteRevision: value.RouteRevision,
		AccountID: value.AccountID, AccountRevision: value.AccountRevision,
		CredentialEpoch: value.CredentialEpoch,
		ObservedAt:      value.ObservedAt, ExpiresAt: value.ExpiresAt,
		Method: value.Method, StatusCode: value.StatusCode,
		Scheme: value.Scheme, Authority: value.Authority,
		Path: value.Path, RawQuery: value.RawQuery,
		ContentType: value.ContentType, ContentEncoding: value.ContentEncoding,
		Representation:   value.Representation,
		Canonicalization: value.Canonicalization,
		HeaderCount:      value.HeaderCount, TrailerCount: value.TrailerCount,
		BodyBytes: value.BodyBytes, BodySHA256: digest,
		DigestScope: value.DigestScope, PayloadState: value.PayloadState,
		PayloadReason: value.PayloadReason, ContainsSecret: value.ContainsSecret,
		RevealAvailable: value.PayloadState == rawevidence.PayloadCaptured ||
			value.PayloadState == rawevidence.PayloadTruncated,
	}
}
