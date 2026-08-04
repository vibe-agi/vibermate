package capturecontrol

import (
	"errors"
	"path/filepath"
	"time"

	"github.com/vibe-agi/vibermate/internal/capturerun"
	"github.com/vibe-agi/vibermate/internal/clientadapter"
)

const ClientAdapterSourcePrelaunchDigestCatalog = "prelaunch_digest_catalog"

// CaptureRunView is the exact additionalProperties:false representation used
// by the contracted create, attach-process, and heartbeat responses. Lifecycle
// state and traffic observation belong to the Desktop audit projection, not
// this launcher-facing wire.
type CaptureRunView struct {
	ID                  string                        `json:"id"`
	ExecutableLabel     string                        `json:"executableLabel"`
	CWD                 string                        `json:"cwd"`
	ProcessID           int                           `json:"processId,omitempty"`
	CreatedAt           time.Time                     `json:"createdAt"`
	ExpiresAt           time.Time                     `json:"expiresAt"`
	ClientAdapterState  clientadapter.Status          `json:"clientAdapterState"`
	ClientRecognition   clientadapter.Recognition     `json:"clientRecognition"`
	CatalogRevision     clientadapter.CatalogRevision `json:"catalogRevision"`
	ClientAdapter       *ClientAdapterView            `json:"clientAdapter,omitempty"`
	ClientAdapterReason string                        `json:"clientAdapterReason,omitempty"`
}

// ClientAdapterView intentionally projects only fields admitted by the wire
// schema. In particular, releaseSha256 and features remain internal evidence.
type ClientAdapterView struct {
	ID              string                        `json:"id"`
	Revision        clientadapter.AdapterRevision `json:"revision"`
	Version         string                        `json:"version"`
	CatalogRevision clientadapter.CatalogRevision `json:"catalogRevision"`
	Source          string                        `json:"source"`
	InstallShape    clientadapter.InstallShape    `json:"installShape"`
	LaunchRecipe    clientadapter.LaunchRecipe    `json:"launchRecipe"`
}

// ClientLaunchAdapterView adds only the exact-version behavior the launcher
// must enforce. CaptureRun audit projections intentionally retain the smaller
// identity-only ClientAdapterView.
type ClientLaunchAdapterView struct {
	ClientAdapterView
	StreamingFallbackPolicy clientadapter.StreamingFallbackPolicy `json:"streamingFallbackPolicy"`
}

// ClientSignerView freezes the signer wire independently of the internal
// evidence type so future verifier fields cannot silently expand the grant.
type ClientSignerView struct {
	ID              string                        `json:"id"`
	Revision        clientadapter.AdapterRevision `json:"revision"`
	CatalogRevision clientadapter.CatalogRevision `json:"catalogRevision"`
	InstallShape    clientadapter.InstallShape    `json:"installShape"`
	LaunchRecipe    clientadapter.LaunchRecipe    `json:"launchRecipe"`
	SignedPath      string                        `json:"signedPath"`
}

// LaunchGrant is the one-time launcher response. Both capabilities are
// sensitive and memory-only; none of the nested evidence includes internal
// release digests or feature flags.
type LaunchGrant struct {
	Run             CaptureRunView                `json:"run"`
	CatalogRevision clientadapter.CatalogRevision `json:"catalogRevision"`
	Recognition     clientadapter.Recognition     `json:"recognition"`
	Adapter         *ClientLaunchAdapterView      `json:"adapter,omitempty"`
	Signer          *ClientSignerView             `json:"signer,omitempty"`
	LaunchRecipe    clientadapter.LaunchRecipe    `json:"launchRecipe"`
	ExecutablePath  string                        `json:"executablePath"`
	ProxyAddress    string                        `json:"proxyAddress"`
	ProxyToken      string                        `json:"proxyToken"`
	RunCapability   string                        `json:"runCapability"`
	RootPEMPath     string                        `json:"rootPemPath,omitempty"`
	// ProtectedAuthorities is the exact MITM/NO_PROXY boundary. A member does
	// not by itself authorize replacing the client's model-service credential.
	ProtectedAuthorities []string `json:"protectedAuthorities"`
	// ManagedCredentialAuthorities is the subset for which this launch may replace
	// ambient client authentication with a non-provider local placeholder.
	ManagedCredentialAuthorities []string `json:"managedCredentialAuthorities"`
}

// CaptureRunViewOf projects a trusted lifecycle view onto the contracted
// launcher wire. A persisted run is either verified or generic; failed adapter
// detection is rejected before the lifecycle creates a run.
func CaptureRunViewOf(view capturerun.View) CaptureRunView {
	state := clientadapter.StatusGeneric
	if view.Adapter != nil {
		state = clientadapter.StatusVerified
	}
	return CaptureRunView{
		ID:                 view.ID,
		ExecutableLabel:    view.ExecutableLabel,
		CWD:                view.CWD,
		ProcessID:          view.ProcessID,
		CreatedAt:          view.CreatedAt,
		ExpiresAt:          view.ExpiresAt,
		ClientAdapterState: state,
		ClientRecognition:  capturerun.NormalizedRecognition(view.Recognition),
		CatalogRevision:    view.CatalogRevision,
		ClientAdapter:      clientAdapterViewOf(view.Adapter),
	}
}

func clientAdapterViewOf(
	evidence *clientadapter.Evidence,
) *ClientAdapterView {
	if evidence == nil {
		return nil
	}
	return &ClientAdapterView{
		ID:              evidence.ID,
		Revision:        evidence.Revision,
		Version:         evidence.Version,
		CatalogRevision: evidence.CatalogRevision,
		Source:          ClientAdapterSourcePrelaunchDigestCatalog,
		InstallShape:    evidence.InstallShape,
		LaunchRecipe:    evidence.LaunchRecipe,
	}
}

func clientLaunchAdapterViewOf(
	evidence *clientadapter.Evidence,
) *ClientLaunchAdapterView {
	identity := clientAdapterViewOf(evidence)
	if identity == nil {
		return nil
	}
	return &ClientLaunchAdapterView{
		ClientAdapterView: *identity,
		StreamingFallbackPolicy: clientadapter.
			StreamingFallbackPolicyOf(evidence),
	}
}

func clientSignerViewOf(
	evidence *clientadapter.SignerEvidence,
) *ClientSignerView {
	if evidence == nil {
		return nil
	}
	return &ClientSignerView{
		ID:              evidence.ID,
		Revision:        evidence.Revision,
		CatalogRevision: evidence.CatalogRevision,
		InstallShape:    evidence.InstallShape,
		LaunchRecipe:    evidence.LaunchRecipe,
		SignedPath:      evidence.SignedPath,
	}
}

func (view CaptureRunView) validate() error {
	if view.ID == "" || view.ExecutableLabel == "" || view.CWD == "" ||
		view.ProcessID < 0 || view.CreatedAt.IsZero() || view.ExpiresAt.IsZero() ||
		!view.ExpiresAt.After(view.CreatedAt) ||
		!view.CatalogRevision.Valid() || !view.ClientRecognition.Valid() {
		return errors.New("CaptureRun view is incomplete")
	}
	switch view.ClientAdapterState {
	case clientadapter.StatusVerified:
		if view.ClientAdapter == nil ||
			view.ClientRecognition != clientadapter.RecognitionVerified {
			return errors.New("CaptureRun verified adapter state is inconsistent")
		}
	case clientadapter.StatusGeneric:
		if view.ClientAdapter != nil ||
			view.ClientRecognition == clientadapter.RecognitionVerified {
			return errors.New("CaptureRun generic adapter state is inconsistent")
		}
	case clientadapter.StatusFailed:
		if view.ClientAdapter != nil {
			return errors.New("CaptureRun failed adapter state carries evidence")
		}
	default:
		return errors.New("CaptureRun adapter state is invalid")
	}
	if view.ClientAdapter != nil {
		if err := view.ClientAdapter.validate(); err != nil ||
			view.ClientAdapter.CatalogRevision != view.CatalogRevision {
			return errors.New("CaptureRun adapter evidence is inconsistent")
		}
	}
	return nil
}

func (evidence ClientAdapterView) validate() error {
	if evidence.ID == "" || evidence.Version == "" ||
		!evidence.Revision.Valid() || !evidence.CatalogRevision.Valid() ||
		evidence.Source != ClientAdapterSourcePrelaunchDigestCatalog ||
		!evidence.InstallShape.Valid() || !evidence.LaunchRecipe.Valid() ||
		evidence.LaunchRecipe == clientadapter.LaunchGeneric {
		return errors.New("client adapter wire evidence is invalid")
	}
	return nil
}

func (evidence ClientLaunchAdapterView) validate() error {
	if err := evidence.ClientAdapterView.validate(); err != nil ||
		!evidence.StreamingFallbackPolicy.Valid() {
		return errors.New("client launch adapter wire evidence is invalid")
	}
	if evidence.StreamingFallbackPolicy ==
		clientadapter.StreamingFallbackCoreOwned &&
		(evidence.ID != "claude-code" ||
			evidence.LaunchRecipe != clientadapter.LaunchNodeEnvProxy) {
		return errors.New("client adapter fallback policy is unsupported")
	}
	return nil
}

func (evidence ClientSignerView) validate() error {
	if evidence.ID == "" || !evidence.Revision.Valid() ||
		!evidence.CatalogRevision.Valid() || !evidence.InstallShape.Valid() ||
		!evidence.LaunchRecipe.Valid() ||
		evidence.LaunchRecipe == clientadapter.LaunchGeneric ||
		evidence.SignedPath == "" || !filepath.IsAbs(evidence.SignedPath) {
		return errors.New("client signer wire evidence is invalid")
	}
	return nil
}

// Validate is the one place a launch grant's producer and launcher consumer
// agree on evidence, recognition, recipe, and contracted run metadata.
func (grant LaunchGrant) Validate() error {
	if err := grant.Run.validate(); err != nil ||
		!grant.CatalogRevision.Valid() || !grant.LaunchRecipe.Valid() ||
		!grant.Recognition.Valid() || grant.ExecutablePath == "" ||
		grant.ProxyAddress == "" || grant.ProxyToken == "" ||
		grant.RunCapability == "" ||
		grant.ProxyToken == grant.RunCapability ||
		grant.ProtectedAuthorities == nil ||
		grant.ManagedCredentialAuthorities == nil ||
		grant.Run.CatalogRevision != grant.CatalogRevision ||
		grant.Run.ClientRecognition != grant.Recognition {
		return errors.New("CaptureRun launch grant is incomplete")
	}
	if grant.Adapter != nil && grant.Signer != nil {
		return errors.New(
			"CaptureRun launch grant carries release and signer evidence at once",
		)
	}
	switch {
	case grant.Adapter != nil:
		if err := grant.Adapter.validate(); err != nil ||
			grant.Adapter.CatalogRevision != grant.CatalogRevision ||
			grant.Adapter.LaunchRecipe != grant.LaunchRecipe ||
			grant.LaunchRecipe == clientadapter.LaunchGeneric ||
			grant.Recognition != clientadapter.RecognitionVerified ||
			grant.Run.ClientAdapter == nil ||
			*grant.Run.ClientAdapter != grant.Adapter.ClientAdapterView {
			return errors.New(
				"CaptureRun launch grant adapter evidence is inconsistent",
			)
		}
	case grant.Signer != nil:
		if err := grant.Signer.validate(); err != nil ||
			grant.Signer.CatalogRevision != grant.CatalogRevision ||
			grant.Signer.LaunchRecipe != grant.LaunchRecipe ||
			grant.LaunchRecipe == clientadapter.LaunchGeneric ||
			grant.Recognition != clientadapter.RecognitionRecognized ||
			grant.Run.ClientAdapter != nil {
			return errors.New(
				"CaptureRun launch grant signer evidence is inconsistent",
			)
		}
	default:
		if grant.LaunchRecipe != clientadapter.LaunchGeneric ||
			grant.Run.ClientAdapter != nil {
			return errors.New(
				"CaptureRun launch grant omitted the evidence its recipe rests on",
			)
		}
	}
	if grant.LaunchRecipe.RequiresRoot() {
		if grant.RootPEMPath == "" {
			return errors.New("CaptureRun launch grant is missing the local Root")
		}
	} else if grant.RootPEMPath != "" {
		return errors.New("generic CaptureRun launch grant carries a local Root")
	}
	protected := make(map[string]struct{}, len(grant.ProtectedAuthorities))
	for _, authority := range grant.ProtectedAuthorities {
		if authority == "" {
			return errors.New("CaptureRun protected authority is empty")
		}
		protected[authority] = struct{}{}
	}
	managed := make(
		map[string]struct{},
		len(grant.ManagedCredentialAuthorities),
	)
	for _, authority := range grant.ManagedCredentialAuthorities {
		if _, allowed := protected[authority]; !allowed {
			return errors.New(
				"CaptureRun managed authority is outside the protected set",
			)
		}
		if _, duplicate := managed[authority]; duplicate {
			return errors.New("CaptureRun managed authority is duplicated")
		}
		managed[authority] = struct{}{}
	}
	return nil
}
