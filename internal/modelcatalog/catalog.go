package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vibe-agi/vibermate/internal/provideraccount"
	"github.com/vibe-agi/vibermate/internal/providerauth"
	"github.com/vibe-agi/vibermate/internal/upstreamendpoint"
)

const (
	// Discovery includes the host-backed credential read before the network
	// request. Security.framework can take longer than five seconds even when it
	// returns without authentication UI, so this budget must cover both phases.
	defaultEndpointTimeout = 20 * time.Second
	defaultSnapshotTTL     = 45 * time.Second
)

type Options struct {
	Endpoints   EndpointReader
	Credentials CredentialAuthority
	Transport   EndpointTransport
	Clock       Clock
	Timeout     time.Duration
	CacheTTL    time.Duration
}

type Service struct {
	endpoints   EndpointReader
	credentials CredentialAuthority
	transport   EndpointTransport
	clock       Clock
	timeout     time.Duration
	cacheTTL    time.Duration

	mu    sync.Mutex
	cache map[cacheKey]cachedSnapshot
}

type cacheKey struct {
	endpointID upstreamendpoint.ID
	accountID  provideraccount.ID
}

type cachedSnapshot struct {
	endpointRevision uint64
	accountRevision  uint64
	credentialEpoch  uint64
	expiresAt        time.Time
	snapshot         Snapshot
}

func New(options Options) (*Service, error) {
	if options.Endpoints == nil || options.Credentials == nil ||
		options.Transport == nil || options.Clock == nil {
		return nil, errors.New("model catalog dependencies are incomplete")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultEndpointTimeout
	}
	cacheTTL := options.CacheTTL
	if cacheTTL == 0 {
		cacheTTL = defaultSnapshotTTL
	}
	if timeout < 0 || cacheTTL < 0 {
		return nil, errors.New("model catalog timing is invalid")
	}
	return &Service{
		endpoints:   options.Endpoints,
		credentials: options.Credentials,
		transport:   options.Transport,
		clock:       options.Clock,
		timeout:     timeout,
		cacheTTL:    cacheTTL,
		cache:       make(map[cacheKey]cachedSnapshot),
	}, nil
}

// Discover returns the model strings this Endpoint currently advertises.
// Refresh bypasses the short-lived Endpoint snapshot. Model IDs are opaque
// Endpoint-owned values and are returned exactly as advertised.
func (service *Service) Discover(
	ctx context.Context,
	endpointID upstreamendpoint.ID,
	accountID provideraccount.ID,
	refresh bool,
) (Snapshot, error) {
	if service == nil || ctx == nil || endpointID == "" || accountID == "" {
		return Snapshot{}, ErrInvalidCatalog
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	endpoint, err := service.endpoints.Get(ctx, endpointID)
	if err != nil {
		return Snapshot{}, err
	}
	if endpoint.Validate() != nil {
		return Snapshot{}, ErrInvalidCatalog
	}
	if endpoint.State != upstreamendpoint.StateActive {
		return Snapshot{}, upstreamendpoint.ErrEndpointDisabled
	}
	credential, err := service.credentials.AcquireEndpointCredential(
		ctx,
		accountID,
		endpoint,
	)
	if err != nil {
		return Snapshot{}, err
	}
	defer credential.Release()
	if credential.Mode() != providerauth.CredentialManaged {
		return Snapshot{}, ErrInvalidCatalog
	}
	accountRef, hasAccount := credential.Account()
	if !hasAccount || accountRef.Validate() != nil ||
		accountRef.ID != accountID.String() {
		return Snapshot{}, ErrInvalidCatalog
	}
	key := cacheKey{endpointID: endpointID, accountID: accountID}
	now := service.clock.Now().UTC()
	if !refresh {
		service.mu.Lock()
		cached, found := service.cache[key]
		service.mu.Unlock()
		if found && cached.endpointRevision == endpoint.Revision &&
			cached.accountRevision == accountRef.Revision &&
			cached.credentialEpoch == accountRef.CredentialEpoch &&
			now.Before(cached.expiresAt) {
			return cached.snapshot.clone(), nil
		}
	}

	discoveryContext, cancelDiscovery := service.remoteContext(ctx)
	liveModels, liveErr := service.discoverEndpoint(
		discoveryContext,
		endpoint,
		credential,
	)
	cancelDiscovery()
	snapshot := Snapshot{
		EndpointID:       endpoint.ID,
		EndpointRevision: endpoint.Revision,
		AccountID:        accountID,
		AccountRevision:  accountRef.Revision,
		CredentialEpoch:  accountRef.CredentialEpoch,
		ObservedAt:       now,
		Models:           make([]Model, 0, len(liveModels)),
	}
	if liveErr != nil {
		return Snapshot{}, fmt.Errorf("%w: %w", ErrCatalogUnavailable, liveErr)
	}
	snapshot.AvailabilitySource = AvailabilitySourceEndpoint
	for _, live := range liveModels {
		model := live
		model.VerifiedAvailable = true
		snapshot.Models = append(snapshot.Models, model)
	}

	service.mu.Lock()
	service.cache[key] = cachedSnapshot{
		endpointRevision: endpoint.Revision,
		accountRevision:  accountRef.Revision,
		credentialEpoch:  accountRef.CredentialEpoch,
		expiresAt:        now.Add(service.cacheTTL),
		snapshot:         snapshot.clone(),
	}
	service.mu.Unlock()
	return snapshot.clone(), nil
}

func (service *Service) remoteContext(parent context.Context) (context.Context, context.CancelFunc) {
	if service.timeout > 0 {
		return context.WithTimeout(parent, service.timeout)
	}
	return context.WithCancel(parent)
}

type endpointCatalog struct {
	Data   []endpointModel `json:"data"`
	Models []endpointModel `json:"models"`
}

type endpointModel struct {
	ID              string `json:"id"`
	DisplayName     string `json:"display_name"`
	OwnedBy         string `json:"owned_by"`
	MaxModelLength  int64  `json:"max_model_len"`
	ContextWindow   int64  `json:"context_window"`
	MaxOutputTokens int64  `json:"max_output_tokens"`
}

func (service *Service) discoverEndpoint(
	ctx context.Context,
	endpoint upstreamendpoint.Endpoint,
	credential providerauth.Lease,
) ([]Model, error) {
	response, err := service.transport.FetchEndpointModels(ctx, endpoint, credential)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, &EndpointHTTPError{StatusCode: response.StatusCode}
	}
	limited := io.LimitReader(response.Body, MaxCatalogBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil || len(body) == 0 || len(body) > MaxCatalogBodyBytes || !utf8.Valid(body) {
		return nil, ErrInvalidCatalog
	}
	var catalog endpointCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, ErrInvalidCatalog
	}
	items := catalog.Data
	if items == nil {
		items = catalog.Models
	}
	if items == nil || len(items) > MaxCatalogModels {
		return nil, ErrInvalidCatalog
	}
	models := make([]Model, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if !validModelID(item.ID) || !validModelName(item.DisplayName) ||
			!validModelName(item.OwnedBy) || item.MaxModelLength < 0 || item.ContextWindow < 0 ||
			item.MaxOutputTokens < 0 {
			return nil, ErrInvalidCatalog
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, ErrInvalidCatalog
		}
		seen[item.ID] = struct{}{}
		contextLimit := item.MaxModelLength
		if contextLimit == 0 {
			contextLimit = item.ContextWindow
		}
		models = append(models, Model{
			ID: item.ID, DisplayName: item.DisplayName, OwnedBy: item.OwnedBy,
			ContextLimit: contextLimit, OutputLimit: item.MaxOutputTokens,
		})
	}
	sort.Slice(models, func(left, right int) bool { return models[left].ID < models[right].ID })
	return models, nil
}

func validMetadata(metadata Metadata) bool {
	if !validMetadataID(metadata.CanonicalID) || !validModelName(metadata.DisplayName) ||
		!validDescription(metadata.Description) || !validModelName(metadata.Family) ||
		metadata.ContextLimit < 0 || metadata.OutputLimit < 0 {
		return false
	}
	for _, modality := range append(append([]string(nil), metadata.InputModalities...), metadata.OutputModalities...) {
		if !validModelName(modality) {
			return false
		}
	}
	return true
}
