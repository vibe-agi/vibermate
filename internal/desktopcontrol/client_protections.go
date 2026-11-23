package desktopcontrol

import (
	"errors"
	"net/http"
	"strings"

	"github.com/vibe-agi/vibermate/internal/captureassignment"
	"github.com/vibe-agi/vibermate/internal/captureidentity"
	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
	"github.com/vibe-agi/vibermate/internal/environment"
	"github.com/vibe-agi/vibermate/internal/manualcapture"
	"github.com/vibe-agi/vibermate/internal/workspaceidentity"
)

// ClientProtectionsResponse lists the client protections one Capture's client
// receives, and whether each acts on the routing set the Capture uses now
// (ADR 0026). The Runtime that applies the data-plane protections computes
// it, so the App renders it without a copy of the rules. An older Runtime has
// no such route, and the App then shows no protections.
type ClientProtectionsResponse struct {
	CaptureKey string `json:"captureKey"`
	// Client is the catalogued client ID the run was verified or recognized
	// as. It is omitted for a generic run and for a manual Capture.
	Client      string                     `json:"client,omitempty"`
	Protections []ClientProtectionResponse `json:"protections"`
}

// ClientProtectionResponse is one protection. Every value comes from a closed
// set. Reason is present only when the protection is not on.
type ClientProtectionResponse struct {
	// ID is the protection's Feature name, for example
	// structured_workspace_tools.
	ID        string                  `json:"id"`
	Basis     ClientProtectionBasis   `json:"basis"`
	State     ClientProtectionState   `json:"state"`
	Reason    ClientProtectionReason  `json:"reason,omitempty"`
	AppliedBy ClientProtectionApplier `json:"appliedBy"`
}

// ClientProtectionBasis says why the run's client receives a protection.
type ClientProtectionBasis string

const (
	// The run's digest-verified release carries the protection.
	ClientProtectionExactRelease ClientProtectionBasis = "exact_release"
	// The Runtime recognized the run's client by its publisher's signature,
	// and the recognized-client policy grants the protection.
	ClientProtectionRecognizedClient ClientProtectionBasis = "recognized_client"
	// The operation catalog applies the protection to every client.
	ClientProtectionOperationCatalog ClientProtectionBasis = "operation_catalog"
)

// ClientProtectionState says whether a protection the client receives acts
// on this Capture.
type ClientProtectionState string

const (
	ClientProtectionOn ClientProtectionState = "on"
	// The routing set gives the protection nothing to act on.
	ClientProtectionInactive ClientProtectionState = "inactive"
	// The protection is withheld from this run.
	ClientProtectionOff ClientProtectionState = "off"
)

// ClientProtectionReason says why a protection is not on.
type ClientProtectionReason string

const (
	// The routing set sends the client's requests to their original
	// destination, where ViberMate holds no stream and decides no tool.
	ClientProtectionOriginalDestination ClientProtectionReason = "original_destination"
	// File tools are classified only in a workspace a local launcher proved.
	ClientProtectionRemoteWorkspace ClientProtectionReason = "remote_workspace"
	// The Runtime's operator switch withdrew the recognized-basis protection.
	ClientProtectionOperatorDisabled ClientProtectionReason = "operator_disabled"
)

// ClientProtectionApplier says which process applies a protection.
type ClientProtectionApplier string

const (
	// The vibermate command sets it in the client's environment at launch.
	// The command's own operator switch governs it, not the Runtime's, and the
	// command's notice is what says it was set.
	ClientProtectionLauncher ClientProtectionApplier = "launcher"
	// This Runtime applies it to each connection or request.
	ClientProtectionRuntime ClientProtectionApplier = "runtime"
)

// clientProtectionOrder lists, in the order of the response, the Features a
// tier can carry that a reader acts on. FeatureResponsesWebSocketHTTPFallback
// is not among them: the operation catalog answers every client's upgrade,
// so the policy table lists that protection as unconditional, and a verified
// release's bit adds no second row. Unconditional protections follow.
var clientProtectionOrder = [...]clientadapter.Feature{
	clientadapter.FeatureCoreOwnedStreamingFallback,
	clientadapter.FeatureStructuredWorkspaceTools,
	clientadapter.FeatureResponsesDataKeepalive,
}

// clientProtection is one protection the run's tier names, before the
// Capture's assignment and routing set decide its state.
type clientProtection struct {
	feature clientadapter.Feature
	basis   ClientProtectionBasis
}

// clientProtectionsNamedBy returns the client a run's own tier names and the
// protections that tier carries. A verified run never falls through to the
// recognized basis. A generic run, and a recognized run stored under another
// catalog revision (which the Runtime's index does not name), get nothing.
func clientProtectionsNamedBy(view capturerun.View) (string, []clientProtection) {
	var client string
	var features clientadapter.Feature
	var basis ClientProtectionBasis
	switch {
	case view.Adapter != nil:
		client, features, basis = view.Adapter.ID, view.Adapter.Features, ClientProtectionExactRelease
	case view.RecognizedClient.ID != "":
		// Policy, not Protections: a protection the operator switch withdrew
		// is listed as off rather than left out.
		client, features, basis = view.RecognizedClient.ID, view.RecognizedClient.Policy, ClientProtectionRecognizedClient
	default:
		return "", nil
	}
	var protections []clientProtection
	for _, feature := range clientProtectionOrder {
		if features.Includes(feature) {
			protections = append(protections, clientProtection{feature: feature, basis: basis})
		}
	}
	for _, policy := range clientadapter.RecognizedClientPolicies() {
		if policy.ClientID != client {
			continue
		}
		for _, grant := range policy.Unconditional {
			protections = append(protections, clientProtection{feature: grant.Feature, basis: ClientProtectionOperationCatalog})
		}
	}
	return client, protections
}

// clientProtectionsOf decides each protection a run's tier names from the
// Capture's assignment and the routing set it currently uses.
func clientProtectionsOf(
	view capturerun.View,
	assignment captureassignment.Assignment,
	snapshot environment.EnvironmentSnapshot,
) ClientProtectionsResponse {
	client, protections := clientProtectionsNamedBy(view)
	response := ClientProtectionsResponse{
		CaptureKey:  assignment.Capture.Key(),
		Client:      client,
		Protections: make([]ClientProtectionResponse, 0, len(protections)),
	}
	for _, protection := range protections {
		row := ClientProtectionResponse{
			ID: protection.feature.Name(), Basis: protection.basis,
			State: ClientProtectionOn, AppliedBy: ClientProtectionRuntime,
		}
		// The Runtime's switch withdraws only recognized-basis protections
		// the Runtime itself applies.
		withdrawn := protection.basis == ClientProtectionRecognizedClient &&
			!view.RecognizedClient.Protections.Includes(protection.feature)
		switch protection.feature {
		case clientadapter.FeatureCoreOwnedStreamingFallback:
			row.AppliedBy = ClientProtectionLauncher
			if !launcherOwnsStreamFallback(protection.basis, assignment) {
				row.State, row.Reason = ClientProtectionInactive, ClientProtectionOriginalDestination
			}
		case clientadapter.FeatureStructuredWorkspaceTools:
			switch {
			case withdrawn:
				row.State, row.Reason = ClientProtectionOff, ClientProtectionOperatorDisabled
			case view.WorkspaceEvidence != workspaceidentity.EvidenceLocalLauncher:
				row.State, row.Reason = ClientProtectionOff, ClientProtectionRemoteWorkspace
			case !routesUpstream(snapshot, environment.ClientProtocolAnthropicMessages):
				row.State, row.Reason = ClientProtectionInactive, ClientProtectionOriginalDestination
			}
		case clientadapter.FeatureResponsesDataKeepalive:
			switch {
			case withdrawn:
				row.State, row.Reason = ClientProtectionOff, ClientProtectionOperatorDisabled
			case !routesUpstream(snapshot, environment.ClientProtocolOpenAIResponses):
				row.State, row.Reason = ClientProtectionInactive, ClientProtectionOriginalDestination
			}
		}
		response.Protections = append(response.Protections, row)
	}
	return response
}

// launcherOwnsStreamFallback mirrors when the launcher sets the fallback
// instruction. A verified release carries its adapter, and so its policy,
// into every launch whose authority protects an endpoint. A recognized
// Claude Code receives the instruction only when its own target is an
// authority whose every route is Upstream, compared as the launcher compares
// it. The launcher reads the target from the child environment it prepares,
// while the assignment holds the one the issuer resolved from what the
// command reported, so the command's notice stays what says the instruction
// was set.
func launcherOwnsStreamFallback(
	basis ClientProtectionBasis,
	assignment captureassignment.Assignment,
) bool {
	if basis == ClientProtectionExactRelease {
		return len(assignment.LaunchAuthority.ProtectedAuthorities()) != 0
	}
	if !assignment.ClientTarget.Available() {
		return false
	}
	target := assignment.ClientTarget.ActualOrigin().EndpointAuthority()
	for _, authority := range assignment.LaunchAuthority.ManagedCredentialAuthorities() {
		if strings.EqualFold(authority, target) {
			return true
		}
	}
	return false
}

// routesUpstream reports whether the routing set sends a request of this
// protocol to an Upstream route, where the Exchange holds the stream and
// decides tools. An Original Destination plan has neither.
func routesUpstream(
	snapshot environment.EnvironmentSnapshot,
	protocol environment.ClientProtocol,
) bool {
	for _, endpoint := range snapshot.ClientEndpoints() {
		for _, plan := range endpoint.ProtocolPlans() {
			if plan.ClientProtocol == protocol &&
				plan.Destination.Kind == environment.DestinationKindUpstream &&
				plan.Destination.Upstream != nil &&
				len(plan.Destination.Upstream.Routes) != 0 {
				return true
			}
		}
	}
	return false
}

// getCaptureClientProtections answers which protections a Capture's client
// receives. A manual Capture names no client, so it receives none, and
// neither does a run without an assignment.
func (handler *Handler) getCaptureClientProtections(writer http.ResponseWriter, request *http.Request) {
	if request.URL.RawQuery != "" {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	reference, err := captureidentity.ParseKey(request.PathValue("captureKey"))
	if err != nil {
		writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
		return
	}
	none := ClientProtectionsResponse{
		CaptureKey:  reference.Key(),
		Protections: []ClientProtectionResponse{},
	}
	if reference.Kind == captureidentity.KindManualCapture {
		if handler.manualCaptures == nil {
			writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
			return
		}
		id, parseErr := manualcapture.ParseID(reference.ID)
		if parseErr != nil {
			writeProblem(writer, http.StatusUnprocessableEntity, ReasonInvalidRequest)
			return
		}
		if _, getErr := handler.manualCaptures.Get(request.Context(), manualcapture.NewLocalOwnerScope(), id); getErr != nil {
			writeCaptureReadError(writer, getErr)
			return
		}
		writeJSON(writer, http.StatusOK, none)
		return
	}
	if handler.captureRuns == nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
		return
	}
	view, err := handler.captureRuns.GetRun(request.Context(), reference.ID)
	if err != nil {
		writeCaptureReadError(writer, err)
		return
	}
	client, protections := clientProtectionsNamedBy(view)
	none.Client = client
	if len(protections) == 0 {
		writeJSON(writer, http.StatusOK, none)
		return
	}
	assignment, err := handler.assignments.Resolve(request.Context(), reference)
	if errors.Is(err, captureassignment.ErrAssignmentNotFound) {
		writeJSON(writer, http.StatusOK, none)
		return
	}
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
		return
	}
	// The current revision, not the launch one: an applied later revision
	// decides what each new connection and request receives.
	snapshot, err := handler.environments.ResolveRevision(
		request.Context(), assignment.EnvironmentID, assignment.EnvironmentRevision,
	)
	if err != nil {
		writeProblem(writer, http.StatusServiceUnavailable, ReasonCaptureUnavailable)
		return
	}
	writeJSON(writer, http.StatusOK, clientProtectionsOf(view, assignment, snapshot))
}
