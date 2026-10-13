package desktopcontrol

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/vibe-agi/vibermate/internal/egressnetwork"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/vlesscore"
	"github.com/vibe-agi/vibermate/internal/vlessprofile"
)

type EgressProfileResponse struct {
	ID          string               `json:"id"`
	Revision    uint64               `json:"revision"`
	DisplayName string               `json:"displayName"`
	Policy      egressnetwork.Policy `json:"policy"`
	PublishedAt string               `json:"publishedAt"`
}

type EgressProfileListResponse struct {
	Items []EgressProfileResponse `json:"items"`
}

type egressProfileInput struct {
	DisplayName string               `json:"displayName"`
	Policy      egressnetwork.Policy `json:"policy"`
}

type vlessProfileInput struct {
	ID            string                       `json:"id"`
	DisplayName   string                       `json:"displayName"`
	URI           string                       `json:"uri"`
	TLS           json.RawMessage              `json:"tls"`
	Resolver      egressnetwork.ResolverPolicy `json:"resolver"`
	DirectDomains egressnetwork.DirectDomains  `json:"directDomains"`
}

// Private wire input is explicit because core TLSOptions redacts JSON.
type vlessTLSInput struct {
	Mode  vlesscore.TrustMode `json:"mode"`
	CAPEM string              `json:"caPem"`
}

func (vlessProfileInput) String() string   { return "desktopcontrol.vlessProfileInput{redacted}" }
func (vlessProfileInput) GoString() string { return "desktopcontrol.vlessProfileInput{redacted}" }
func (vlessTLSInput) String() string       { return "desktopcontrol.vlessTLSInput{redacted}" }
func (vlessTLSInput) GoString() string     { return "desktopcontrol.vlessTLSInput{redacted}" }

func (handler *Handler) importVLESSProfile(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	defer clear(body)
	var input vlessProfileInput
	var tlsInput vlessTLSInput
	var tlsOptions *vlesscore.TLSOptions
	defer func() {
		input.URI = ""
		clear(input.TLS)
		input.TLS = nil
		tlsInput = vlessTLSInput{}
		tlsOptions = nil
	}()
	decodeErr := bodyErr
	if decodeErr == nil {
		decodeErr = decodeStrictJSON(body, &input)
	}
	if decodeErr == nil && len(input.TLS) != 0 {
		decodeErr = decodeStrictJSON(input.TLS, &tlsInput)
		if decodeErr == nil {
			options := vlesscore.TLSOptions{Mode: tlsInput.Mode, CAPEM: tlsInput.CAPEM}
			decodeErr = options.Validate()
			tlsOptions = &options
		}
	}
	id, idErr := egressprofile.NewID(input.ID)
	if headerErr != nil || decodeErr != nil || idErr != nil || request.URL.RawQuery != "" || expected > uint64(egressprofile.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := codeLibraryFingerprint(request, expected, body)
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		profile, importErr := handler.vlessProfiles.Import(request.Context(), vlessprofile.ImportCommand{ID: id, ExpectedRevision: egressprofile.Revision(expected), DisplayName: input.DisplayName, URI: input.URI, TLS: tlsOptions, Resolver: input.Resolver, DirectDomains: input.DirectDomains})
		input.URI = ""
		clear(input.TLS)
		input.TLS = nil
		tlsInput = vlessTLSInput{}
		tlsOptions = nil
		if importErr != nil {
			if errors.Is(importErr, vlessprofile.ErrInvalidURI) {
				return problemResponse(problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest, detail: "VLESS import requires raw/TCP, TLS or REALITY, and none encryption; REALITY requires Vision and TLS requires valid trust settings."})
			}
			if errors.Is(importErr, vlessprofile.ErrUnavailable) {
				return problemResponse(problemSpec{status: http.StatusServiceUnavailable, reason: ReasonEgressProfileUnavailable})
			}
			return problemResponse(classifyEgressProfileError(importErr))
		}
		return jsonResponse(http.StatusOK, egressProfileResponseOf(profile))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonEgressProfileConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) getVLESSSettings(writer http.ResponseWriter, request *http.Request) {
	id, idErr := egressprofile.NewID(request.PathValue("egressId"))
	revision, revisionErr := strconv.ParseUint(request.PathValue("egressRevision"), 10, 64)
	if idErr != nil || revisionErr != nil || revision == 0 || revision > uint64(egressprofile.MaxRevision) || request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	description, err := handler.vlessProfiles.Describe(request.Context(), id, egressprofile.Revision(revision))
	if err != nil {
		spec := classifyEgressProfileError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, description)
}

func (handler *Handler) listEgressProfiles(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	profiles, err := handler.egressProfiles.List(request.Context())
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonEgressProfileUnavailable)
		return
	}
	response := EgressProfileListResponse{Items: make([]EgressProfileResponse, len(profiles))}
	for index, profile := range profiles {
		response.Items[index] = egressProfileResponseOf(profile)
	}
	writeJSON(writer, http.StatusOK, response)
}

func (handler *Handler) publishEgressProfile(writer http.ResponseWriter, request *http.Request) {
	expected, key, headerErr := mutationHeaders(request)
	body, bodyErr := readJSONBody(request)
	var input egressProfileInput
	decodeErr := bodyErr
	if decodeErr == nil {
		decodeErr = decodeStrictJSON(body, &input)
	}
	id, idErr := egressprofile.NewID(request.PathValue("egressId"))
	if headerErr != nil || decodeErr != nil || idErr != nil || request.URL.RawQuery != "" ||
		expected > uint64(egressprofile.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := codeLibraryFingerprint(request, expected, body)
	response, err := handler.idempotent.execute(request.Context(), key, fingerprint, func() cachedResponse {
		profile, publishErr := handler.egressProfiles.Publish(
			request.Context(),
			egressprofile.PublishCommand{
				ID: id, ExpectedRevision: egressprofile.Revision(expected),
				DisplayName: input.DisplayName, Policy: input.Policy,
			},
		)
		if publishErr != nil {
			return problemResponse(classifyEgressProfileError(publishErr))
		}
		return jsonResponse(http.StatusOK, egressProfileResponseOf(profile))
	})
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonEgressProfileConflict)
		return
	}
	writeCached(writer, response)
}

func (handler *Handler) getEgressProfileRevision(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	id, idErr := egressprofile.NewID(request.PathValue("egressId"))
	revision, revisionErr := strconv.ParseUint(request.PathValue("egressRevision"), 10, 64)
	if idErr != nil || revisionErr != nil || revision == 0 ||
		revision > uint64(egressprofile.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	profile, err := handler.egressProfiles.GetRevision(
		request.Context(), id, egressprofile.Revision(revision),
	)
	if err != nil {
		spec := classifyEgressProfileError(err)
		writeProblem(writer, spec.status, spec.reason)
		return
	}
	writeJSON(writer, http.StatusOK, egressProfileResponseOf(profile))
}

func egressProfileResponseOf(profile egressprofile.ProfileRevision) EgressProfileResponse {
	return EgressProfileResponse{
		ID: profile.ID.String(), Revision: uint64(profile.Revision),
		DisplayName: profile.DisplayName, Policy: profile.Policy,
		PublishedAt: profile.PublishedAt.Format(time.RFC3339Nano),
	}
}

func classifyEgressProfileError(err error) problemSpec {
	switch {
	case errors.Is(err, egressprofile.ErrInvalidProfile):
		return problemSpec{status: http.StatusUnprocessableEntity, reason: ReasonInvalidRequest}
	case errors.Is(err, egressprofile.ErrProfileNotFound):
		return problemSpec{status: http.StatusNotFound, reason: ReasonEgressProfileNotFound}
	case errors.Is(err, egressprofile.ErrRevisionConflict):
		return problemSpec{status: http.StatusConflict, reason: ReasonEgressProfileConflict}
	default:
		return problemSpec{status: http.StatusServiceUnavailable, reason: ReasonEgressProfileUnavailable}
	}
}
