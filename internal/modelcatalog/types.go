// Package modelcatalog exposes two deliberately separate catalogs: exact,
// live model IDs advertised by an upstream Endpoint, and descriptive client
// model metadata from models.dev. Neither catalog is allowed to reinterpret
// identifiers owned by the other.
package modelcatalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const (
	// Model IDs are persisted in Environment model mappings as exact, opaque
	// owner-defined strings capped at 256 bytes. Discovery must never
	// advertise an ID that cannot be selected and saved.
	MaxModelIDBytes     = 256
	MaxMetadataIDBytes  = 512
	MaxModelNameBytes   = 512
	MaxDescriptionBytes = 8 << 10
	MaxCatalogModels    = 16 << 10
	MaxCatalogBodyBytes = 4 << 20
	// The provider-independent models.dev directory can grow independently of
	// any one Endpoint catalog. Keep its ceiling separate from live discovery
	// without allowing a remote metadata provider to return an unbounded body.
	MaxMetadataBodyBytes       = 16 << 20
	AvailabilitySourceEndpoint = "endpoint"
	MetadataSourceModelsDev    = "models.dev"
)

var (
	ErrInvalidCatalog     = errors.New("model catalog is invalid")
	ErrCatalogUnavailable = errors.New("model catalog is unavailable")
)

// EndpointHTTPError retains only the non-secret HTTP status returned by the
// selected Endpoint. It lets the control plane distinguish an authentication
// rejection from transport failure without retaining an upstream response body.
type EndpointHTTPError struct {
	StatusCode int
}

func (err *EndpointHTTPError) Error() string {
	return fmt.Sprintf("model endpoint returned HTTP %d", err.StatusCode)
}

type EndpointReader interface {
	Get(context.Context, upstreamendpoint.ID) (upstreamendpoint.Endpoint, error)
}

// EndpointTransport is the only network boundary used for live model
// discovery. Implementations are responsible for Offline Hold, audit, and the
// provider transport policy; the catalog only parses the returned document.
type EndpointTransport interface {
	FetchEndpointModels(
		context.Context,
		upstreamendpoint.Endpoint,
		providerauth.Lease,
	) (*http.Response, error)
}

// CredentialAuthority proves that the explicitly selected Account belongs to
// the Endpoint and freezes the Account revision and credential epoch used for
// discovery. Client-passthrough credentials cannot satisfy this boundary.
type CredentialAuthority interface {
	AcquireEndpointCredential(
		context.Context,
		provideraccount.ID,
		upstreamendpoint.Endpoint,
	) (providerauth.Lease, error)
}

// MetadataTransport fetches the fixed, descriptive models.dev directory.
// Directory data can enrich an Endpoint model, but never grants availability.
type MetadataTransport interface {
	FetchModelsDev(context.Context) (*http.Response, error)
}

type Clock interface{ Now() time.Time }

// Metadata is descriptive catalog data. CanonicalID is a metadata-directory
// identity, not necessarily the model string accepted by an Endpoint.
type Metadata struct {
	CanonicalID      string
	DisplayName      string
	Description      string
	Family           string
	Reasoning        bool
	ToolCalls        bool
	StructuredOutput bool
	Attachments      bool
	OpenWeights      bool
	ContextLimit     int64
	OutputLimit      int64
	InputModalities  []string
	OutputModalities []string
	KnowledgeCutoff  string
	ReleaseDate      string
}

func (metadata Metadata) clone() Metadata {
	metadata.InputModalities = slices.Clone(metadata.InputModalities)
	metadata.OutputModalities = slices.Clone(metadata.OutputModalities)
	return metadata
}

type MetadataReader interface {
	// Lookup accepts an exact metadata-directory identity. Implementations must
	// not derive an identity by splitting or rewriting an Endpoint model ID.
	Lookup(context.Context, string) (Metadata, bool, error)
}

// ProviderMetadataReader exposes the request-side model directory grouped by
// the provider namespace owned by models.dev. It is deliberately separate from
// Reader: these entries describe model IDs a client may send, while Reader
// proves the exact opaque model IDs accepted by one selected upstream Endpoint.
type ProviderMetadataReader interface {
	ListProvider(context.Context, string) ([]Metadata, error)
}

type Reader interface {
	Discover(
		context.Context,
		upstreamendpoint.ID,
		provideraccount.ID,
		bool,
	) (Snapshot, error)
}

type Model struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	OwnedBy           string `json:"ownedBy"`
	VerifiedAvailable bool   `json:"verifiedAvailable"`
	ContextLimit      int64  `json:"contextLimit"`
	OutputLimit       int64  `json:"outputLimit"`
}

func (model Model) clone() Model {
	return model
}

type Snapshot struct {
	EndpointID         upstreamendpoint.ID `json:"-"`
	EndpointRevision   uint64              `json:"endpointRevision"`
	AccountID          provideraccount.ID  `json:"-"`
	AccountRevision    uint64              `json:"accountRevision"`
	CredentialEpoch    uint64              `json:"credentialEpoch"`
	ObservedAt         time.Time           `json:"observedAt"`
	AvailabilitySource string              `json:"availabilitySource"`
	Models             []Model             `json:"models"`
}

func (snapshot Snapshot) clone() Snapshot {
	models := snapshot.Models
	snapshot.Models = make([]Model, len(models))
	for index, model := range models {
		snapshot.Models[index] = model.clone()
	}
	return snapshot
}

func validModelID(value string) bool {
	if value == "" || len(value) > MaxModelIDBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\uFEFF' {
			return false
		}
	}
	return true
}

func validProviderID(value string) bool {
	return validText(value, MaxModelIDBytes) && !strings.Contains(value, "/")
}

func validMetadataID(value string) bool {
	return validText(value, MaxMetadataIDBytes)
}

func validModelName(value string) bool {
	return value == "" || validText(value, MaxModelNameBytes)
}

func validDescription(value string) bool {
	return value == "" || validText(value, MaxDescriptionBytes)
}

func validText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || character == '\uFEFF' {
			return false
		}
	}
	return true
}
