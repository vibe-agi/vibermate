// Package environment owns the editable Environment aggregate and the
// immutable, Environment-scoped authority compiled from it.
package environment

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/codelibrary"
	"github.com/vibe-agi/vibermate/internal/egressprofile"
	"github.com/vibe-agi/vibermate/internal/originidentity"
	"github.com/vibe-agi/vibermate/internal/protocolspec"
)

const (
	MaxIDBytes                = 128
	MaxNameBytes              = 256
	MaxOriginBytes            = 2048
	MaxRevision               = Revision(1<<63 - 1)
	MaxCaptureImpact          = 1024
	MaxRetiredChildIdentities = 4096
	MaxLaunchEnvironmentRules = 128
	MaxEnvironmentValueBytes  = 32 << 10
	MaxLaunchEnvironmentBytes = 64 << 10
)

var (
	ErrInvalidEnvironment     = errors.New("Environment is invalid")
	ErrInvalidTransition      = errors.New("Environment transition is invalid")
	ErrEnvironmentNotFound    = errors.New("Environment is not configured")
	ErrEnvironmentDisabled    = errors.New("Environment is disabled")
	ErrProjectionUnavailable  = errors.New("Environment projection is unavailable")
	ErrProjectionNotRestored  = errors.New("Environment projection is not restored")
	ErrProjectionRestored     = errors.New("Environment projection was already restored")
	ErrRevisionConflict       = errors.New("Environment revision conflict")
	ErrDraftNotFound          = errors.New("Environment draft is not configured")
	ErrPreviewStale           = errors.New("Environment impact preview is stale")
	ErrWriteNotCommitted      = errors.New("Environment write was not committed")
	ErrCommitOutcomeUnknown   = errors.New("Environment commit outcome is unknown")
	ErrSystemEnvironment      = errors.New("system_transparent is Core-owned")
	ErrImpactLimitExceeded    = errors.New("Environment impact inspection exceeded its bound")
	ErrTransitionUnavailable  = errors.New("Environment transition cannot be coordinated safely")
	ErrInvalidRepositoryState = errors.New("Environment repository state is invalid")
)

type Revision uint64

type EnvironmentID string
type ClientEndpointID string
type ClientProtocolPlanID string
type UpstreamRouteID string

type ChildIdentityKind string

const (
	ChildIdentityClientEndpoint     ChildIdentityKind = "client_endpoint"
	ChildIdentityClientProtocolPlan ChildIdentityKind = "client_protocol_plan"
	ChildIdentityUpstreamRoute      ChildIdentityKind = "upstream_route"
)

func NewEnvironmentID(value string) (EnvironmentID, error) {
	if err := validateID("Environment ID", value); err != nil {
		return "", err
	}
	return EnvironmentID(value), nil
}

func NewClientEndpointID(value string) (ClientEndpointID, error) {
	if err := validateID("ClientEndpoint ID", value); err != nil {
		return "", err
	}
	return ClientEndpointID(value), nil
}

func NewClientProtocolPlanID(value string) (ClientProtocolPlanID, error) {
	if err := validateID("ClientProtocolPlan ID", value); err != nil {
		return "", err
	}
	return ClientProtocolPlanID(value), nil
}

func NewUpstreamRouteID(value string) (UpstreamRouteID, error) {
	if err := validateID("UpstreamRoute ID", value); err != nil {
		return "", err
	}
	return UpstreamRouteID(value), nil
}

func (id EnvironmentID) String() string        { return string(id) }
func (id ClientEndpointID) String() string     { return string(id) }
func (id ClientProtocolPlanID) String() string { return string(id) }
func (id UpstreamRouteID) String() string      { return string(id) }

func validateID(label, value string) error {
	if value == "" || len(value) > MaxIDBytes || !utf8.ValidString(value) ||
		strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s is not canonical", ErrInvalidEnvironment, label)
	}
	for index, character := range value {
		if unicode.IsControl(character) || character > unicode.MaxASCII ||
			!(character >= 'a' && character <= 'z') &&
				!(character >= '0' && character <= '9') &&
				character != '-' && character != '_' && character != '.' {
			return fmt.Errorf("%w: %s contains a non-canonical character at %d", ErrInvalidEnvironment, label, index)
		}
	}
	first := value[0]
	if !((first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return fmt.Errorf("%w: %s must begin with an ASCII letter or digit", ErrInvalidEnvironment, label)
	}
	return nil
}

type State string

const (
	StateActive   State = "active"
	StateDisabled State = "disabled"
)

type ClientProtocol string

const (
	ClientProtocolAnthropicMessages ClientProtocol = "anthropic_messages"
	ClientProtocolOpenAIResponses   ClientProtocol = "openai_responses"
	ClientProtocolOpenAIChat        ClientProtocol = "openai_chat"
)

type DestinationKind string

const (
	DestinationKindOriginal DestinationKind = "original"
	DestinationKindUpstream DestinationKind = "upstream"
)

type AccountSelectionMode string

const (
	AccountSelectionFixed      AccountSelectionMode = "fixed"
	AccountSelectionJavaScript AccountSelectionMode = "javascript"
)

// ContentRecordingMode controls the separate conversation-evidence plane.
// Activity, ConnectionEvent, and EgressAttempt remain body-free in every
// mode; this policy never weakens their redaction contract.
type ContentRecordingMode string

const (
	ContentRecordingFull         ContentRecordingMode = "full"
	ContentRecordingMetadataOnly ContentRecordingMode = "metadata_only"
	ContentRecordingOff          ContentRecordingMode = "off"

	DefaultContentRetentionDays uint16 = 30
	MaxContentRetentionDays     uint16 = 3650
)

type ContentRecordingPolicy struct {
	Mode          ContentRecordingMode `json:"mode"`
	RetentionDays uint16               `json:"retentionDays"`
}

// ToolPolicyMode controls whether semantic tool intents are only observed,
// held for a person, or rejected unless they match a Core-proven safe action.
// The zero persisted representation is deliberately the default Observe
// policy, so an Environment that does not opt in to enforcement cannot begin
// interrupting an Agent after restart.
type ToolPolicyMode string

const (
	ToolPolicyObserve ToolPolicyMode = "observe"
	ToolPolicyReview  ToolPolicyMode = "review"
	ToolPolicyStrict  ToolPolicyMode = "strict"
)

type PolicySet struct {
	ToolMode ToolPolicyMode `json:"toolMode"`
}

func DefaultPolicySet() PolicySet {
	return PolicySet{ToolMode: ToolPolicyObserve}
}

func (policy PolicySet) Validate() error {
	switch policy.ToolMode {
	case ToolPolicyObserve, ToolPolicyReview, ToolPolicyStrict:
		return nil
	default:
		return fmt.Errorf("%w: tool policy mode is unsupported", ErrInvalidEnvironment)
	}
}

func DefaultContentRecordingPolicy() ContentRecordingPolicy {
	return ContentRecordingPolicy{
		Mode:          ContentRecordingFull,
		RetentionDays: DefaultContentRetentionDays,
	}
}

func (policy ContentRecordingPolicy) Validate() error {
	switch policy.Mode {
	case ContentRecordingFull, ContentRecordingMetadataOnly:
		if policy.RetentionDays == 0 ||
			policy.RetentionDays > MaxContentRetentionDays {
			return fmt.Errorf(
				"%w: content retention is outside the supported range",
				ErrInvalidEnvironment,
			)
		}
	case ContentRecordingOff:
		if policy.RetentionDays != 0 {
			return fmt.Errorf(
				"%w: disabled content recording retains content",
				ErrInvalidEnvironment,
			)
		}
	default:
		return fmt.Errorf(
			"%w: content recording mode is unsupported",
			ErrInvalidEnvironment,
		)
	}
	return nil
}

// Environment is the complete user-editable aggregate. Draft is lifecycle
// metadata and is deliberately not a State value.
type Environment struct {
	ID                EnvironmentID           `json:"id"`
	Name              string                  `json:"name"`
	State             State                   `json:"state"`
	Revision          Revision                `json:"revision"`
	ClientEndpoints   []ClientEndpoint        `json:"clientEndpoints"`
	PluginBindings    []PluginBinding         `json:"pluginBindings"`
	BudgetPolicy      BudgetPolicy            `json:"budgetPolicy"`
	ContentRecording  ContentRecordingPolicy  `json:"contentRecording"`
	LaunchEnvironment LaunchEnvironmentPolicy `json:"launchEnvironment"`
	// PolicySet is nil only in canonical storage for the default Observe
	// policy. EffectivePolicySet always returns the concrete authority.
	PolicySet              *PolicySet             `json:"policySet,omitempty"`
	RetiredChildIdentities []RetiredChildIdentity `json:"retiredChildIdentities,omitempty"`
}

// LaunchEnvironmentPolicy is the exact child-process environment overlay
// frozen with one Environment revision. It never mutates the Runtime Server or
// launcher process. DeleteEnv and SetEnv are disjoint; launcher-owned routing,
// trust, and credential variables are not configurable through this policy.
type LaunchEnvironmentPolicy struct {
	SetEnv    map[string]string `json:"setEnv,omitempty"`
	DeleteEnv []string          `json:"deleteEnv,omitempty"`
}

func (environment Environment) EffectivePolicySet() PolicySet {
	if environment.PolicySet == nil {
		return DefaultPolicySet()
	}
	return *environment.PolicySet
}

// RetiredChildIdentity is Core-owned aggregate history. Control-plane input
// cannot create or remove these records. Keeping the tombstone with the
// aggregate makes deletion followed by reuse of the same stable ID
// unrepresentable across later revisions and normal reopen.
type RetiredChildIdentity struct {
	Kind              ChildIdentityKind `json:"kind"`
	ID                string            `json:"id"`
	ParentID          string            `json:"parentId"`
	RetiredAtRevision Revision          `json:"retiredAtRevision"`
}

type ClientEndpoint struct {
	ID            ClientEndpointID            `json:"id"`
	Revision      Revision                    `json:"revision"`
	ClientOrigin  originidentity.ClientOrigin `json:"clientOrigin"`
	ProtocolPlans []ClientProtocolPlan        `json:"protocolPlans"`
}

type ClientProtocolPlan struct {
	ID                  ClientProtocolPlanID            `json:"id"`
	Revision            Revision                        `json:"revision"`
	ClientProtocol      ClientProtocol                  `json:"clientProtocol"`
	ClientAdapterPolicy ClientAdapterPolicy             `json:"clientAdapterPolicy"`
	Destination         DestinationPlan                 `json:"destination"`
	EgressProfile       egressprofile.ProfileRevision   `json:"egressProfile"`
	Transforms          []codelibrary.TransformRevision `json:"transforms"`
	PluginBindings      []PluginBinding                 `json:"pluginBindings"`
}

// DestinationPlan is a closed choice. Original intentionally has no payload:
// the client-owned origin, authentication, and model remain authoritative.
// Upstream is present only when Kind is DestinationKindUpstream.
type DestinationPlan struct {
	Kind     DestinationKind `json:"kind"`
	Upstream *UpstreamPlan   `json:"upstream,omitempty"`
}

type UpstreamPlan struct {
	Routes         []UpstreamRoute `json:"routes"`
	DefaultRouteID UpstreamRouteID `json:"defaultRouteId"`
	RouteSet       RouteSet        `json:"routeSet"`
}

type UpstreamRoute struct {
	ID              UpstreamRouteID    `json:"id"`
	Revision        Revision           `json:"revision"`
	ProviderTarget  ProviderTarget     `json:"providerTarget"`
	BackendProtocol string             `json:"backendProtocol"`
	AccountPolicy   RouteAccountPolicy `json:"accountPolicy"`
	ModelPolicy     ModelPolicy        `json:"modelPolicy"`
	WireProfileRef  string             `json:"wireProfileRef"`
	PluginBindings  []PluginBinding    `json:"pluginBindings"`
}

type RouteAccountPolicy struct {
	Revision       Revision                             `json:"revision"`
	Mode           AccountSelectionMode                 `json:"mode"`
	FixedAccountID string                               `json:"fixedAccountId,omitempty"`
	Selector       *codelibrary.AccountSelectorRevision `json:"selector,omitempty"`
	Accounts       []RouteAccountReference              `json:"accounts"`
}

type RouteAccountReference struct {
	ID          string   `json:"id"`
	Revision    Revision `json:"revision"`
	DisplayName string   `json:"displayName"`
}

// The first slice keeps these lower-level policies typed without assigning
// them runtime behavior that belongs to later ProviderAccount/plugin slices.
type PluginBinding struct {
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
	PluginID string   `json:"pluginId"`
}

type BudgetPolicy struct {
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
}

type ClientAdapterPolicy struct {
	ID       string   `json:"id"`
	Revision Revision `json:"revision"`
}

type RouteSet struct {
	ID                string            `json:"id"`
	Revision          Revision          `json:"revision"`
	CandidateRouteIDs []UpstreamRouteID `json:"candidateRouteIds"`
}

type ProviderTarget struct {
	ID           string                            `json:"id"`
	Revision     Revision                          `json:"revision"`
	Origin       originidentity.ProviderOrigin     `json:"origin"`
	RealmID      string                            `json:"realmId"`
	Capabilities []protocolspec.ProviderCapability `json:"capabilities"`
}

type ModelMode string

const (
	ModelModePassthrough ModelMode = "passthrough"
	ModelModeMap         ModelMode = "map"
)

// ModelMapping is an exact, route-scoped request-model rewrite. Both values
// are opaque provider identifiers; ViberMate never infers a vendor from their
// spelling.
type ModelMapping struct {
	RequestedModel string `json:"requestedModel"`
	UpstreamModel  string `json:"upstreamModel"`
}

type ModelPolicy struct {
	Revision Revision       `json:"revision"`
	Mode     ModelMode      `json:"mode"`
	Mappings []ModelMapping `json:"mappings"`
}

// ResolveMapping returns the exact upstream model configured for requested.
// The identifier is deliberately opaque: matching is byte-for-byte and an
// absent mapping means the client model must be preserved.
func (policy ModelPolicy) ResolveMapping(requested string) (string, bool) {
	for _, mapping := range policy.Mappings {
		if mapping.RequestedModel == requested {
			return mapping.UpstreamModel, true
		}
	}
	return "", false
}

// AccountDescriptor is non-secret catalog evidence used only to reject route
// references that cannot be interpreted by their declared realm/protocol.
type AccountDescriptor struct {
	ID                       string
	Revision                 Revision
	DisplayName              string
	UpstreamEndpointID       string
	UpstreamEndpointRevision Revision
	RealmID                  string
	Active                   bool
	BackendProtocols         []string
}

type AccountCatalog interface {
	LookupAccount(string) (AccountDescriptor, bool)
}
