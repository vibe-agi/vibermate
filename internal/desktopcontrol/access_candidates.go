package desktopcontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/vibe-agi/vibermate/internal/access"
	"github.com/vibe-agi/vibermate/internal/accessapply"
	"github.com/vibe-agi/vibermate/internal/activity"
	"github.com/vibe-agi/vibermate/internal/secretstore"
)

const candidateIDRandomBytes = 18

type AccessCandidateProvider string

const (
	AccessCandidateProviderOpenAI              AccessCandidateProvider = "openai"
	AccessCandidateProviderOpenAICompatible    AccessCandidateProvider = "openai-compatible"
	AccessCandidateProviderAnthropic           AccessCandidateProvider = "anthropic"
	AccessCandidateProviderAnthropicCompatible AccessCandidateProvider = "anthropic-compatible"
)

// AddAccessCandidateInput contains only public routing metadata. Resource IDs
// and the SecretRef are generated inside the native control plane and never
// need to cross the browser boundary.
type AddAccessCandidateInput struct {
	Name                 string                  `json:"name"`
	Provider             AccessCandidateProvider `json:"provider"`
	BaseURL              string                  `json:"baseUrl,omitempty"`
	Model                string                  `json:"model"`
	AuthDriverRef        string                  `json:"authDriverRef,omitempty"`
	UpstreamPresentation string                  `json:"upstreamPresentation,omitempty"`
}

type AccessCandidateRefResponse struct {
	ProfileID    string `json:"profileId"`
	CredentialID string `json:"credentialId"`
}

type AccessAddCandidateResponse struct {
	Outcome          access.WriteOutcome        `json:"outcome"`
	Revision         access.Revision            `json:"revision"`
	ApplicationState AccessApplicationState     `json:"applicationState"`
	PlanHash         string                     `json:"planHash,omitempty"`
	Candidate        AccessCandidateRefResponse `json:"candidate"`
}

type generatedCandidateIDs struct {
	profile    access.EndpointProfileID
	target     access.ProviderTargetID
	credential access.AccountBindingID
	secret     access.SecretRef
}

func (handler *Handler) addAccessCandidate(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expected, key, err := mutationHeaders(request)
	if err != nil || expected >= uint64(access.MaxRevision) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	body, err := readJSONBody(request)
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256(bytes.Join(
		[][]byte{
			[]byte(request.Method),
			[]byte(request.URL.Path),
			[]byte(strconv.FormatUint(expected, 10)),
			body,
		},
		[]byte{0},
	))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			var input AddAccessCandidateInput
			if decodeStrictJSON(body, &input) != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			accessID, parseErr := access.NewAccessID(request.PathValue("accessId"))
			if parseErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			aggregate, spec := handler.readAggregateForMutation(
				request.Context(),
				accessID,
				access.Revision(expected),
			)
			if spec != nil {
				return problemResponse(*spec)
			}
			candidate, buildErr := buildAddedCandidate(aggregate, input)
			if buildErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			ids, idErr := newCandidateIDs(aggregate)
			if idErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusServiceUnavailable,
					reason: ReasonRuntimeUnavailable,
				})
			}
			command := appendCandidateCommand(
				aggregate,
				access.Revision(expected),
				candidate,
				ids,
			)
			return handler.writeAddedCandidateMutation(
				request.Context(),
				command,
				AccessCandidateRefResponse{
					ProfileID:    ids.profile.String(),
					CredentialID: ids.credential.String(),
				},
				http.StatusCreated,
			)
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

type addedCandidate struct {
	name        string
	origin      access.ProviderOrigin
	model       access.ModelName
	authDriver  access.AuthDriverRef
	wireProfile access.UpstreamWireProfileRef
	backend     access.Dialect
}

func buildAddedCandidate(
	aggregate access.Aggregate,
	input AddAccessCandidateInput,
) (addedCandidate, error) {
	if aggregate.Binding.Status != access.AccessStatusEnabled ||
		len(aggregate.Profiles) >= access.MaxEndpointProfiles ||
		len(aggregate.ProviderTargets) >= access.MaxEndpointProfiles ||
		len(aggregate.AccountBindings) >= access.MaxAccountBindings {
		return addedCandidate{}, access.ErrInvalidAccess
	}
	model, err := access.NewModelName(input.Model)
	if err != nil {
		return addedCandidate{}, err
	}
	var (
		rawOrigin   string
		backend     access.Dialect
		defaultAuth access.AuthDriverRef
	)
	switch input.Provider {
	case AccessCandidateProviderOpenAI:
		if input.BaseURL != "" {
			return addedCandidate{}, access.ErrInvalidAccess
		}
		rawOrigin = "https://api.openai.com/v1"
		backend = access.DialectOpenAIChat
		defaultAuth = access.StaticHeaderAuthDriverRef()
	case AccessCandidateProviderOpenAICompatible:
		if input.BaseURL == "" {
			return addedCandidate{}, access.ErrInvalidAccess
		}
		rawOrigin = input.BaseURL
		backend = access.DialectOpenAIChat
		defaultAuth = access.StaticHeaderAuthDriverRef()
	case AccessCandidateProviderAnthropic:
		if input.BaseURL != "" {
			return addedCandidate{}, access.ErrInvalidAccess
		}
		rawOrigin = "https://api.anthropic.com"
		backend = access.DialectAnthropicMessages
		defaultAuth = access.AnthropicAPIKeyAuthDriverRef()
	case AccessCandidateProviderAnthropicCompatible:
		if input.BaseURL == "" {
			return addedCandidate{}, access.ErrInvalidAccess
		}
		rawOrigin = input.BaseURL
		backend = access.DialectAnthropicMessages
		defaultAuth = access.AnthropicAPIKeyAuthDriverRef()
	default:
		return addedCandidate{}, access.ErrInvalidAccess
	}
	if !candidateBackendSupported(
		aggregate.AgentEndpoint.ClientDialect,
		backend,
	) {
		return addedCandidate{}, access.ErrInvalidAccess
	}
	authDriver := defaultAuth
	switch input.AuthDriverRef {
	case "":
	case access.AuthDriverAnthropicAPIKeyValue:
		authDriver = access.AnthropicAPIKeyAuthDriverRef()
	case access.AuthDriverStaticHeaderValue:
		authDriver = access.StaticHeaderAuthDriverRef()
	default:
		return addedCandidate{}, access.ErrInvalidAccess
	}
	origin, err := access.NewProviderOrigin(rawOrigin)
	if err != nil {
		return addedCandidate{}, err
	}
	origin, err = normalizeCandidateOrigin(origin, backend)
	if err != nil {
		return addedCandidate{}, err
	}
	wireProfile := access.FollowClientUpstreamWireProfileRef()
	switch input.UpstreamPresentation {
	case "", access.UpstreamWireProfileFollowClientValue:
	case access.UpstreamWireProfileClaudeCodeValue:
		wireProfile = access.ClaudeCodeUpstreamWireProfileRef()
	default:
		return addedCandidate{}, access.ErrInvalidAccess
	}
	return addedCandidate{
		name:        input.Name,
		origin:      origin,
		model:       model,
		authDriver:  authDriver,
		wireProfile: wireProfile,
		backend:     backend,
	}, nil
}

// normalizeCandidateOrigin accepts the endpoint URLs people commonly copy
// from provider documentation while keeping protocol codecs responsible for
// their fixed relative path. Anthropic appends v1/messages; OpenAI Chat
// appends chat/completions.
func normalizeCandidateOrigin(
	origin access.ProviderOrigin,
	backend access.Dialect,
) (access.ProviderOrigin, error) {
	basePath := origin.BasePath()
	switch backend {
	case access.DialectAnthropicMessages:
		if strings.HasSuffix(basePath, "/v1/messages") {
			basePath = strings.TrimSuffix(basePath, "/v1/messages")
		} else if strings.HasSuffix(basePath, "/v1") {
			basePath = strings.TrimSuffix(basePath, "/v1")
		}
	case access.DialectOpenAIChat:
		if strings.HasSuffix(basePath, "/chat/completions") {
			basePath = strings.TrimSuffix(basePath, "/chat/completions")
		}
	default:
		return access.ProviderOrigin{}, access.ErrInvalidAccess
	}
	prefix := origin.String()
	if origin.BasePath() != "" {
		prefix = strings.TrimSuffix(prefix, origin.BasePath())
	}
	return access.NewProviderOrigin(prefix + basePath)
}

func candidateBackendSupported(client, backend access.Dialect) bool {
	switch client {
	case access.DialectAnthropicMessages:
		return backend == access.DialectAnthropicMessages ||
			backend == access.DialectOpenAIChat
	case access.DialectOpenAIResponses:
		return backend == access.DialectOpenAIChat
	default:
		return false
	}
}

func appendCandidateCommand(
	aggregate access.Aggregate,
	expected access.Revision,
	candidate addedCandidate,
	ids generatedCandidateIDs,
) access.WriteCommand {
	next := expected + 1
	updated := aggregate.Clone()
	updated.Binding.Revision = next
	updated.Binding.ProfileIDs = append(updated.Binding.ProfileIDs, ids.profile)
	updated.Profiles = append(updated.Profiles, access.EndpointProfile{
		ID:                     ids.profile,
		Revision:               next,
		AccessID:               updated.Binding.ID,
		Kind:                   access.EndpointProfileManaged,
		CredentialSource:       access.CredentialSourceManagedAccount,
		ProcessingMode:         access.ProfileProcessingManaged,
		Name:                   candidate.name,
		BackendDialect:         candidate.backend,
		TargetID:               ids.target,
		UpstreamWireProfileRef: candidate.wireProfile,
		DefaultModelPolicy: access.ModelPolicy{
			Revision:   next,
			Mode:       access.ModelPolicyModeFixed,
			FixedModel: candidate.model,
		},
		AccountBindingIDs:       []access.AccountBindingID{ids.credential},
		DefaultAccountBindingID: ids.credential,
	})
	updated.ProviderTargets = append(updated.ProviderTargets, access.ProviderTarget{
		ID:        ids.target,
		Revision:  next,
		AccessID:  updated.Binding.ID,
		ProfileID: ids.profile,
		Origin:    candidate.origin,
		Protocol:  candidate.backend,
		Capabilities: []access.ProviderCapability{
			access.ProviderCapabilityMessages,
			access.ProviderCapabilityStreaming,
			access.ProviderCapabilityToolCalls,
		},
	})
	updated.AccountBindings = append(
		updated.AccountBindings,
		access.ProviderAccountBinding{
			ID:            ids.credential,
			Revision:      next,
			AccessID:      updated.Binding.ID,
			ProfileID:     ids.profile,
			Label:         candidate.name,
			SecretRef:     ids.secret,
			AuthDriverRef: candidate.authDriver,
			// A staged account is visible to the credential controller but cannot
			// enter a RouteSet until select-candidate verifies and enables it.
			Enabled: false,
		},
	)
	return access.WriteCommand{ExpectedRevision: expected, Aggregate: updated}
}

func newCandidateIDs(aggregate access.Aggregate) (generatedCandidateIDs, error) {
	for attempt := 0; attempt < 8; attempt++ {
		raw := make([]byte, candidateIDRandomBytes)
		if _, err := io.ReadFull(rand.Reader, raw); err != nil {
			return generatedCandidateIDs{}, err
		}
		token := base64.RawURLEncoding.EncodeToString(raw)
		profile, profileErr := access.NewEndpointProfileID("profile-" + token)
		target, targetErr := access.NewProviderTargetID("target-" + token)
		credential, credentialErr := access.NewAccountBindingID("account-" + token)
		secret, secretErr := access.NewSecretRef("secret://provider/" + token)
		if errors.Join(profileErr, targetErr, credentialErr, secretErr) != nil {
			return generatedCandidateIDs{}, access.ErrInvalidAccess
		}
		ids := generatedCandidateIDs{
			profile:    profile,
			target:     target,
			credential: credential,
			secret:     secret,
		}
		if !candidateIDsExist(aggregate, ids) {
			return ids, nil
		}
	}
	return generatedCandidateIDs{}, errors.New("generate unique Access candidate IDs")
}

func candidateIDsExist(
	aggregate access.Aggregate,
	ids generatedCandidateIDs,
) bool {
	for _, profile := range aggregate.Profiles {
		if profile.ID == ids.profile {
			return true
		}
	}
	for _, target := range aggregate.ProviderTargets {
		if target.ID == ids.target {
			return true
		}
	}
	for _, account := range aggregate.AccountBindings {
		if account.ID == ids.credential || account.SecretRef == ids.secret {
			return true
		}
	}
	return false
}

func (handler *Handler) selectAccessCandidate(
	writer http.ResponseWriter,
	request *http.Request,
) {
	expected, key, err := mutationHeaders(request)
	if err != nil ||
		expected >= uint64(access.MaxRevision) ||
		!emptyBody(request.Body) {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	fingerprint := sha256.Sum256([]byte(
		request.Method + "\x00" + request.URL.Path + "\x00" +
			strconv.FormatUint(expected, 10),
	))
	response, err := handler.idempotent.execute(
		request.Context(),
		key,
		fingerprint,
		func() cachedResponse {
			accessID, accessErr := access.NewAccessID(request.PathValue("accessId"))
			profileID, profileErr := access.NewEndpointProfileID(
				request.PathValue("profileId"),
			)
			if errors.Join(accessErr, profileErr) != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			aggregate, spec := handler.readAggregateForMutation(
				request.Context(),
				accessID,
				access.Revision(expected),
			)
			if spec != nil {
				return problemResponse(*spec)
			}
			profile, found := candidateProfile(aggregate, profileID)
			if !found {
				return problemResponse(problemSpec{
					status: http.StatusNotFound,
					reason: ReasonAccessNotConfigured,
				})
			}
			var credentialID access.AccountBindingID
			switch profile.Kind {
			case access.EndpointProfileOriginalPassthrough:
				if profile.ID != access.OriginalPassthroughProfileID() ||
					profile.CredentialSource !=
						access.CredentialSourceClientPassthrough {
					return problemResponse(problemSpec{
						status: http.StatusUnprocessableEntity,
						reason: ReasonInvalidRequest,
					})
				}
			case access.EndpointProfileManaged:
				var credentialFound bool
				credentialID, credentialFound = candidateCredential(
					aggregate,
					profileID,
				)
				if !credentialFound {
					return problemResponse(problemSpec{
						status: http.StatusNotFound,
						reason: ReasonCredentialNotFound,
					})
				}
				credential, credentialErr := handler.credentials.GetCredential(
					request.Context(),
					accessID,
					profileID,
					credentialID,
				)
				if credentialErr != nil {
					return problemResponse(classifyCredentialError(credentialErr))
				}
				switch credential.SecretState {
				case secretstore.StateConfigured:
				case secretstore.StateUnavailable:
					return problemResponse(problemSpec{
						status: http.StatusServiceUnavailable,
						reason: ReasonSecretStoreUnavailable,
					})
				default:
					return problemResponse(problemSpec{
						status: http.StatusUnprocessableEntity,
						reason: ReasonCredentialNotConfigured,
					})
				}
			default:
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			command, buildErr := selectCandidateCommand(
				aggregate,
				access.Revision(expected),
				profileID,
				credentialID,
			)
			if buildErr != nil {
				return problemResponse(problemSpec{
					status: http.StatusUnprocessableEntity,
					reason: ReasonInvalidRequest,
				})
			}
			return handler.writeCandidateSelection(
				request.Context(),
				command,
				profileID,
				http.StatusOK,
			)
		},
	)
	if err != nil {
		writeProblem(writer, http.StatusConflict, ReasonRevisionConflict)
		return
	}
	writeCached(writer, response)
}

func candidateProfile(
	aggregate access.Aggregate,
	profileID access.EndpointProfileID,
) (access.EndpointProfile, bool) {
	for _, profile := range aggregate.Profiles {
		if profile.ID == profileID && profile.AccessID == aggregate.Binding.ID {
			return profile, true
		}
	}
	return access.EndpointProfile{}, false
}

func candidateCredential(
	aggregate access.Aggregate,
	profileID access.EndpointProfileID,
) (access.AccountBindingID, bool) {
	for _, profile := range aggregate.Profiles {
		if profile.ID != profileID || profile.AccessID != aggregate.Binding.ID {
			continue
		}
		for _, account := range aggregate.AccountBindings {
			if account.ID == profile.DefaultAccountBindingID &&
				account.ProfileID == profile.ID &&
				account.AccessID == aggregate.Binding.ID {
				return account.ID, true
			}
		}
		return access.AccountBindingID{}, false
	}
	return access.AccountBindingID{}, false
}

func selectCandidateCommand(
	aggregate access.Aggregate,
	expected access.Revision,
	profileID access.EndpointProfileID,
	credentialID access.AccountBindingID,
) (access.WriteCommand, error) {
	next := expected + 1
	updated := aggregate.Clone()
	updated.Binding.Revision = next
	profile, found := candidateProfile(updated, profileID)
	if !found {
		return access.WriteCommand{}, access.ErrInvalidAccess
	}
	if profile.Kind == access.EndpointProfileManaged {
		accountFound := false
		for index := range updated.AccountBindings {
			account := &updated.AccountBindings[index]
			if account.ID == credentialID && account.ProfileID == profileID {
				account.Enabled = true
				account.Revision = next
				accountFound = true
				break
			}
		}
		if !accountFound {
			return access.WriteCommand{}, access.ErrInvalidAccess
		}
	} else if profile.Kind != access.EndpointProfileOriginalPassthrough ||
		credentialID.String() != "" {
		return access.WriteCommand{}, access.ErrInvalidAccess
	}
	for index := range updated.RouteSets {
		routeSet := &updated.RouteSets[index]
		if routeSet.ID != updated.Binding.DefaultRouteSetID {
			continue
		}
		order := make([]access.EndpointProfileID, 0, len(routeSet.CandidateProfileIDs)+1)
		order = append(order, profileID)
		for _, existing := range routeSet.CandidateProfileIDs {
			if existing == profileID {
				continue
			}
			if routeSet.FallbackMode().Allows() &&
				existing == access.OriginalPassthroughProfileID() {
				continue
			}
			order = append(order, existing)
		}
		if profile.Kind == access.EndpointProfileOriginalPassthrough {
			routeSet.Fallback = access.FallbackDisabled
		}
		routeSet.CandidateProfileIDs = order
		routeSet.Revision = next
		return access.WriteCommand{
			ExpectedRevision: expected,
			Aggregate:        updated,
		}, nil
	}
	return access.WriteCommand{}, access.ErrInvalidAccess
}

func (handler *Handler) readAggregateForMutation(
	ctx context.Context,
	accessID access.AccessID,
	expected access.Revision,
) (access.Aggregate, *problemSpec) {
	aggregate, exists, err := handler.accessCatalog.ReadAccess(ctx, accessID)
	if err != nil {
		return access.Aggregate{}, &problemSpec{
			status: http.StatusServiceUnavailable,
			reason: ReasonRuntimeUnavailable,
		}
	}
	if !exists {
		return access.Aggregate{}, &problemSpec{
			status: http.StatusNotFound,
			reason: ReasonAccessNotConfigured,
		}
	}
	if aggregate.Binding.Revision != expected {
		return access.Aggregate{}, &problemSpec{
			status: http.StatusConflict,
			reason: ReasonRevisionConflict,
		}
	}
	return aggregate, nil
}

func (handler *Handler) writeAddedCandidateMutation(
	ctx context.Context,
	command access.WriteCommand,
	candidate AccessCandidateRefResponse,
	status int,
) cachedResponse {
	result, err := handler.accesses.WriteAccess(ctx, command)
	if err != nil {
		if result.Outcome == access.WriteOutcomeCommitted &&
			errors.Is(err, access.ErrProjectionUnavailable) {
			return jsonResponse(status, AccessAddCandidateResponse{
				Outcome:          result.Outcome,
				Revision:         result.Revision,
				ApplicationState: AccessApplicationStateUnavailable,
				Candidate:        candidate,
			})
		}
		return problemResponse(classifyAccessError(err))
	}
	handler.recordActivity(ctx, activity.Event{
		Kind:      activity.KindAccessApplied,
		AccessID:  command.Aggregate.Binding.ID,
		SubjectID: candidate.ProfileID,
		Status:    activity.StatusSucceeded,
	})
	return jsonResponse(status, AccessAddCandidateResponse{
		Outcome:          result.Outcome,
		Revision:         result.Revision,
		ApplicationState: AccessApplicationStateActive,
		PlanHash:         result.PlanHash.String(),
		Candidate:        candidate,
	})
}

func (handler *Handler) writeCandidateSelection(
	ctx context.Context,
	command access.WriteCommand,
	profileID access.EndpointProfileID,
	status int,
) cachedResponse {
	result, err := handler.accesses.WriteAccess(ctx, command)
	if err != nil {
		if result.Outcome == access.WriteOutcomeCommitted &&
			errors.Is(err, access.ErrProjectionUnavailable) {
			return jsonResponse(status, AccessApplyResponse{
				Outcome:          result.Outcome,
				Revision:         result.Revision,
				ApplicationState: AccessApplicationStateUnavailable,
			})
		}
		return problemResponse(classifyAccessError(err))
	}
	handler.recordActivity(ctx, activity.Event{
		Kind:      activity.KindAccessApplied,
		AccessID:  command.Aggregate.Binding.ID,
		SubjectID: profileID.String(),
		Status:    activity.StatusSucceeded,
	})
	return jsonResponse(status, AccessApplyResponse{
		Outcome:          result.Outcome,
		Revision:         result.Revision,
		ApplicationState: AccessApplicationStateActive,
		PlanHash:         result.PlanHash.String(),
	})
}

// preserveExistingAccountSecretRefs makes the legacy full-aggregate apply
// safe for edits: the browser may omit SecretRef and cannot replace it. Account
// topology changes use add-candidate instead, where IDs and refs are generated
// atomically by the server.
func (handler *Handler) preserveExistingAccountSecretRefs(
	ctx context.Context,
	accessIDValue string,
	expected access.Revision,
	input *accessapply.Input,
) *problemSpec {
	// A few focused apply unit tests construct the handler around the write
	// port only. Production construction always supplies the durable catalog.
	if handler.accessCatalog == nil {
		return nil
	}
	accessID, err := access.NewAccessID(accessIDValue)
	if err != nil {
		return &problemSpec{
			status: http.StatusUnprocessableEntity,
			reason: ReasonInvalidRequest,
		}
	}
	existing, exists, err := handler.accessCatalog.ReadAccess(ctx, accessID)
	if err != nil {
		return &problemSpec{
			status: http.StatusServiceUnavailable,
			reason: ReasonRuntimeUnavailable,
		}
	}
	if !exists {
		// Initial provisioning retains its legacy contract. Subsequent account
		// additions must use add-candidate, which never accepts SecretRef.
		return nil
	}
	if existing.Binding.Revision != expected {
		return &problemSpec{
			status: http.StatusConflict,
			reason: ReasonRevisionConflict,
		}
	}
	if len(input.AccountBindings) != len(existing.AccountBindings) {
		return &problemSpec{
			status: http.StatusUnprocessableEntity,
			reason: ReasonInvalidRequest,
		}
	}
	byID := make(map[string]access.ProviderAccountBinding, len(existing.AccountBindings))
	for _, binding := range existing.AccountBindings {
		byID[binding.ID.String()] = binding
	}
	seen := make(map[string]struct{}, len(input.AccountBindings))
	for index := range input.AccountBindings {
		binding := &input.AccountBindings[index]
		existingBinding, found := byID[binding.ID]
		if !found ||
			existingBinding.ProfileID.String() != binding.ProfileID {
			return &problemSpec{
				status: http.StatusUnprocessableEntity,
				reason: ReasonInvalidRequest,
			}
		}
		if _, duplicate := seen[binding.ID]; duplicate {
			return &problemSpec{
				status: http.StatusUnprocessableEntity,
				reason: ReasonInvalidRequest,
			}
		}
		seen[binding.ID] = struct{}{}
		binding.SecretRef = existingBinding.SecretRef.String()
	}
	return nil
}
